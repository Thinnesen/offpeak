# offpeak design

This document is the design for `offpeak`. Read it before you change the API or the planner.
It uses ASD-STE100 Simplified Technical English.

## 1. Purpose

offpeak is a Kubernetes controller. It runs a batch Job at the best time before a deadline.
"Best" is a weighted mix of signals. A signal is a time series, for example:

- The day-ahead electricity price (EUR/MWh) for a bidding zone.
- The grid carbon intensity (gCO2eq/kWh) for a grid zone.
- The AWS spot price (USD/h) for the instance types of a Karpenter NodePool.

Example: "Run this GPU training Job before 20:00 tomorrow. It takes 2 hours. Use the window with the lowest carbon intensity and the lowest spot price."

## 2. What the signals can and cannot tell us

Read this section before you promise a feature. The README must say the same.

| Signal | Future data | Time variation | Notes |
|---|---|---|---|
| Day-ahead electricity price (Energi Data Service, ENTSO-E) | Published by about 13:00 CET for the next day, in 15-minute periods (since 2025-10-01). | Large. | Real forecast up to 11 to 35 hours ahead. After that, use an assumption. |
| CO2 prognosis (Energi Data Service `CO2EmisProg`, DK1/DK2) | Until the end of the current day. | Medium to large. | 5-minute resolution. |
| Carbon intensity forecast (Electricity Maps) | 24 to 72 hours, by plan. | Medium to large. | Needs an API key (paid for forecasts). Global zones. |
| AWS spot price | No forecast. AWS publishes history only (`DescribeSpotPriceHistory`, 90 days). | Small. AWS smooths spot prices since 2017. | Use an assumption from history. Expect small savings. |
| AWS on-demand price | Flat. | None. | Time does not change the cost. offpeak does not model it. |

Conclusion: on AWS, carbon is the strong signal. Spot price is a weak signal. On-premise or homelab clusters pay the electricity price directly, so the day-ahead price is a strong signal there.

### 2.1 Assumptions for unknown future slots

A signal often does not cover the full window to the deadline. offpeak fills the gaps in this order:

1. **Profile assumption.** The source fetches the last 14 days of history. It computes the median value for each hour of the week (168 buckets, in UTC). A future slot without data gets the median of its bucket. The point has `Assumed: true`.
2. **Neutral fill.** If no profile value exists, the planner uses the mean of all known values in the window. A neutral value does not attract or repel the plan.

The status shows the fraction of the planned window that is assumed. The controller plans again at a fixed interval (default 15 minutes). New data (for example the day-ahead auction result) changes the plan until the Job starts.

## 3. API

Group `offpeak.thinnesen.com`, version `v1alpha1`.

### 3.1 SignalSource (cluster-scoped)

The platform team configures signal sources one time. Workloads refer to them by name.
Exactly one provider block must be set (CEL validation).

```yaml
apiVersion: offpeak.thinnesen.com/v1alpha1
kind: SignalSource
metadata:
  name: dk1-carbon
spec:
  energiDataService:
    area: DK1            # DK1 | DK2
    series: CO2Prognosis # DayAheadPrice | CO2Prognosis
---
kind: SignalSource
metadata: { name: de-price }
spec:
  entsoe:
    biddingZone: 10Y1001A1001A82H   # EIC code
    tokenSecretRef: { name: entsoe, key: token }   # Secret in the controller namespace
---
kind: SignalSource
metadata: { name: eu-north-1-spot }
spec:
  awsSpot:
    region: eu-north-1
    productDescription: Linux/UNIX  # default
---
kind: SignalSource
metadata: { name: se3-carbon }
spec:
  electricityMaps:
    zone: SE-SE3
    tokenSecretRef: { name: electricitymaps, key: token }
```

Status: `conditions` (`Ready`), `lastFetchTime` (last successful fetch), `unit`. The DeferrableJob reconciler writes them after each fetch, at most every 5 minutes while nothing changes. A failed status write does not fail the reconcile. A separate SignalSource reconciler is not necessary in v1alpha1.

Secrets: the controller reads Secrets only in its own namespace. It uses the uncached API reader with `get`. RBAC is a namespaced Role, not a ClusterRole. The controller never logs a token or a URL that contains a token.

### 3.2 DeferrableJob (namespaced, short name `djob`)

```yaml
apiVersion: offpeak.thinnesen.com/v1alpha1
kind: DeferrableJob
metadata:
  name: train-model
spec:
  deadline: "2026-10-18T20:00:00+02:00"  # the Job must finish by this time
  notBefore: null                        # optional, defaults to creation time
  estimatedDuration: 2h                  # includes node start-up time
  signals:
    - sourceRef: se3-carbon
      weight: 70
    - sourceRef: eu-north-1-spot
      weight: 30
  karpenter:                             # optional
    nodePool: gpu
    instanceTypes: [g6.xlarge, g6.2xlarge]   # optional, see 5.2
  jobTemplate:                           # batch/v1 JobTemplateSpec
    spec: { ... }
status:
  phase: Waiting          # Waiting | Running | Succeeded | Failed
  plannedStart: "2026-10-18T03:15:00Z"
  latestStart: "2026-10-18T18:00:00Z"
  lastPlanTime: "..."
  estimates:
    - source: se3-carbon
      unit: gCO2eq/kWh
      planned: "21.4"        # mean over the planned run window
      ifStartedNow: "48.0"
      assumedPercent: 0
  savingsPercent: 38       # weighted, vs. start now
  jobName: train-model
  startedAt: null
  conditions: [Planned, SignalsAvailable, DeadlineAtRisk]
```

Validation (CEL and markers):

- `estimatedDuration` > 0.
- `signals` has 1 to 8 entries. `weight` is 1 to 100.
- `notBefore` < `deadline` when both are set.

## 4. Planner (pure Go, package `internal/planner`)

Input: now, notBefore, deadline, duration, slot step (15 minutes), and one `Series` plus weight per signal.
Output: planned start, latest start, per-signal estimates, and flags.

Algorithm:

1. `earliest = max(now, notBefore)`. `latest = deadline - duration`.
2. If `latest <= earliest`, the plan is "start at `earliest`" and `DeadlineAtRisk = true`.
3. Candidates: `earliest`, then each slot boundary (aligned to the step in UTC) up to `latest`, then `latest`.
4. For each candidate and signal: compute the time-weighted mean of the signal over `[start, start+duration)`. Use the gap fill rules in 2.1. The neutral value is the mean of the signal over `[earliest, deadline)`. Record the assumed fraction.
5. For each signal: the scale is the size of the "start now" mean (`|mean(earliest)|`), but at least 10 % of the mean size of all candidate means. The floor keeps a "start now" mean near 0 from turning a small change into a huge saving. A signal with scale 0 has no effect.
6. Score = sum of `weight * mean / scale`. Pick the lowest score. On a tie (difference < 1e-9), pick the earliest candidate. This is the same as the highest weighted saving against "start now" (step 8), so the plan is never worse than "start now", and a signal that barely moves has little effect.
7. Also report the "start now" means, for `ifStartedNow` and `savingsPercent`. "Start now" is the candidate `earliest`.
8. `savingsPercent` is the weighted mean over the signals with data of `(now - planned) / scale * 100`. It is never negative. A signal without data in `[earliest, deadline)` has no estimate and no effect.

The planner is deterministic. It has no I/O and no clock. It has table-driven tests.

## 5. Controller

### 5.1 DeferrableJob reconcile loop

1. Get the DeferrableJob. If `status.jobName` is set, go to step 7.
2. Resolve each SignalSource and build its provider (cached by name and resourceVersion).
3. Fetch each signal for `[now - 14d, deadline]` (the provider decides how much history it needs). Providers use a shared response cache with a TTL (default 10 minutes), so many DeferrableJobs do not cause many API calls.
4. If all signals fail: set `SignalsAvailable=False`. If `now >= latest`, start the Job (the deadline wins). Else requeue after 1 minute.
   If some signals fail: plan with the others and set `SignalsAvailable=False` with the reason.
5. Run the planner. Write status.
6. If `plannedStart <= now + 30s`: create the Job. Else requeue after `min(plannedStart - now, replanInterval)`.
7. Job exists: mirror its state to `phase` (`Running`, `Succeeded`, `Failed`). Do not plan again.

Job creation:

- Name: the DeferrableJob name. Owner reference with `controller: true`. If a Job with the name exists and it is owned by this DeferrableJob, adopt it. If another owner has it, set a condition and stop.
- Copy labels and annotations from `jobTemplate.metadata`. Add label `offpeak.thinnesen.com/deferrable-job: <name>`.
- Apply Karpenter changes (5.2).
- Emit an Event `Started` with the planned estimates.

The reconciler watches DeferrableJobs and owned Jobs. It does not watch SignalSources in v1alpha1.
The clock is injected (`k8s.io/utils/clock`) so tests can control time.

### 5.2 Karpenter integration

Karpenter `NodePool` (`karpenter.sh/v1`) is read as `unstructured`. offpeak does not import the Karpenter module. This keeps the dependency tree small and keeps offpeak compatible with Karpenter v1.x.

- `spec.karpenter.nodePool` set: the NodePool must exist. Else set a condition `KarpenterReady=False` (reason `NodePoolNotFound` or `KarpenterNotInstalled`) and requeue after 1 minute.
- Instance types for the spot signal: `spec.karpenter.instanceTypes` if set. Else the `In` values of the NodePool requirement `node.kubernetes.io/instance-type`. If neither exists, an `awsSpot` signal fails with a clear reason ("set spec.karpenter.instanceTypes").
- Capacity type: the requirement `karpenter.sh/capacity-type` (`In`, `NotIn` or `Exists`). A NodePool without this requirement allows on-demand only, like Karpenter. If spot is not allowed, an `awsSpot` signal has no data and no effect (no fetch, no estimate). The `KarpenterReady` message says so.
- Spot cost model: for each slot, the minimum spot price over the instance types and availability zones. This is close to the Karpenter `price-capacity-optimized` choice. It is an assumption, and the README must say so.
- Pod template changes at Job creation: add node selector `karpenter.sh/nodepool: <name>`. If `instanceTypes` is set, add a required node affinity term on `node.kubernetes.io/instance-type In [...]`. Never override a key that the user already set.

## 6. Providers (package `internal/signal/...`)

Interface:

```go
type Query struct {
    From, To      time.Time
    InstanceTypes []string // awsSpot only
}
type Source interface {
    Fetch(ctx context.Context, q Query) (forecast.Series, error)
}
```

`forecast.Series` has a unit and sorted, non-overlapping points. A point has start, end, value and `Assumed`.

| Provider | Endpoint | Notes |
|---|---|---|
| `energidataservice` | `https://api.energidataservice.dk/dataset/DayAheadPrices` (fields `TimeUTC`, `PriceArea`, `DayAheadPriceEUR`, 15 min) and `.../CO2EmisProg` (fields `Minutes5UTC`, `PriceArea`, `CO2Emission`, 5 min). | No key. Use `start`, `end`, `filter`, `columns`, `sort` and `limit=0` query parameters. Check the parameters against the live API. |
| `entsoe` | `https://web-api.tp.entsoe.eu/api?documentType=A44&in_Domain=X&out_Domain=X&periodStart=yyyyMMddHHmm&periodEnd=...&securityToken=...` | XML `Publication_MarketDocument`. `resolution` PT15M or PT60M. Curve type A03 omits points that repeat the previous value: fill them. An error returns `Acknowledgement_MarketDocument`. The token is a query parameter: never log the URL. |
| `electricitymaps` | `GET https://api.electricitymaps.com/v3/carbon-intensity/forecast?zone=X` with header `auth-token`. The API has no field for another base URL (see `docs/DECISIONS.md`). | The response has the list in `forecast` (v3) or `data` (newer). Accept both. Items: `datetime`, `carbonIntensity`. Hourly. No history: profile assumption is not available, so the planner uses the neutral fill. |
| `awsSpot` | EC2 Query API action `DescribeSpotPriceHistory` (`GET https://ec2.{region}.amazonaws.com/?Action=...&Version=2016-11-15`), paginated with `NextToken`. Signature Version 4 and the credential chain come from the AWS SDK for Go v2 (`aws/signer/v4`, `config`). | Credentials from the default chain only (EKS Pod Identity or IRSA). Never static keys. The records are change events: a price is valid until the next record for the same type and zone. Build hourly history (the lowest price in effect at the start of each hour), then the profile. All future points are `Assumed`. The provider caches the history for 10 minutes. |

Every HTTP provider:

- Uses one shared `http.Client` with a timeout (default 20 s).
- Limits the response body size (for example 32 MiB) with `io.LimitReader`.
- Validates the input (area, zone and EIC formats) before it builds a URL. Builds URLs with `net/url`, never with string concatenation of user input.
- Has tests with `httptest.Server` and recorded fixtures in `testdata/`.

## 7. Security

- No long-lived cloud credentials. AWS uses the default credential chain.
- Controller Pod: non-root, read-only root file system, no privilege escalation, all capabilities dropped, seccomp `RuntimeDefault` (the scaffold has most of this; keep it).
- Image: distroless static, pinned by digest. Builder image pinned by digest.
- RBAC: least privilege. `jobs`: create, get, list, watch, delete (delete only for the spot fallback, section 8.1). `nodepools.karpenter.sh`: get, list, watch. Secrets: `get` in the controller namespace only.
- Network: egress to the provider endpoints only. Document a sample NetworkPolicy.
- CI: actions pinned by SHA, `govulncheck`, `golangci-lint`, Trivy image scan, cosign keyless signing of release images (GitHub OIDC). Dependabot or Renovate for Go modules, actions and images.

## 8. Phase 2: cloud money and global carbon

Goals of phase 2: save cloud money, save carbon, or both. Use only free and open data. Work in any region.
Section 2 still applies: in the cloud, the time of day changes the price very little. The money comes from spot capacity. The deadline gives the slack that makes spot capacity safe.
All new fields are optional additions to `v1alpha1`.

### 8.1 Spot first, on-demand fallback

New fields in `spec.karpenter`:

```yaml
karpenter:
  nodePool: gpu
  capacityType: SpotWithFallback   # Any (default) | Spot | OnDemand | SpotWithFallback
  fallbackMargin: 15m              # default 15m, safety time before the deadline
```

- `Any`: no change to the pod template (current behavior).
- `Spot` or `OnDemand`: add the node selector `karpenter.sh/capacity-type: spot` or `on-demand`. The NodePool must allow it. Else `KarpenterReady=False` (reason `CapacityTypeNotAllowed`).
- `SpotWithFallback`: the NodePool must allow both. Then:
  1. `fallbackAt = deadline - estimatedDuration - fallbackMargin`. This is the last time at which an on-demand run still meets the deadline.
  2. The planner plans the spot attempt with `fallbackAt` as its deadline. So the planner latest start is `fallbackAt - estimatedDuration`.
  3. If that latest start is not after the earliest start, there is no time for a spot attempt. offpeak creates the on-demand Job at once and sets `DeadlineAtRisk=True`.
  4. The first Job (name: the DeferrableJob name) has `capacity-type: spot`.
  5. If the spot Job fails, or it is not complete at `fallbackAt`, offpeak deletes it (background propagation) and creates the fallback Job `<name>-od` with `capacity-type: on-demand`. It does this one time only.
  6. The status gets `attempts`: one entry per Job, with `jobName`, `capacityType`, `startedAt`, `finishedAt` and `result`. `status.jobName` is the current Job. The phase stays `Running` during the change.
- Spot interruptions: for `Spot` and `SpotWithFallback`, offpeak adds a `podFailurePolicy` rule `Ignore` on the pod condition `DisruptionTarget`. Then an interruption does not use up `backoffLimit`. offpeak adds the rule only if the user did not set `podFailurePolicy`. The rule needs `restartPolicy: Never`. With `OnFailure`, offpeak does not add it and writes a condition message.
- Validation (CEL): `fallbackMargin` >= 0. With `SpotWithFallback`, the name has at most 60 characters, so that `<name>-od` is a valid Job name.
- A spot Job that is missing or being deleted counts as gone, and offpeak starts the fallback. The fallback steps are: create or adopt `<name>-od`, then delete the spot Job, then write the status. They are safe to repeat after a failed reconcile. An attempt with the same Job name is updated, not added again.
- If another owner has the name `<name>-od`, offpeak keeps the spot Job, sets `Fallback=False` (reason `NameConflict`) and retries every minute.
- RBAC: Jobs need `delete` for the fallback.
- Known limit: the fallback starts the work again from the start. offpeak does not checkpoint.

### 8.2 Carbon in every cloud region

The user gives a cloud region. offpeak finds the grid and the best free data source for it.

- Table `internal/region`: maps `aws:<region>`, `azure:<region>` and `gcp:<region>` to a grid zone and a coordinate (the data-center area). Start with the main regions of the three clouds. Each row has a source note. An unknown region is a validation error with a clear message.
- New provider `weatherProxy` (Open-Meteo, `https://api.open-meteo.com/v1/forecast`, no key, global, up to 16 days). Fields: `cloudRegion`, or `latitude` and `longitude`. The value is a "clean power index" from the forecast solar radiation and the wind speed at 100 m. Lower is cleaner. It is a proxy, not a measured carbon intensity. The README and the unit (`index`) must say so. Open-Meteo is free for non-commercial use. For commercial use, the README tells the user to host Open-Meteo (open source) and use the controller flag `--open-meteo-url`. The URL is a controller flag, not a user field (see `docs/DECISIONS.md`, `baseURL`).
- New provider `ukCarbonIntensity` (National Grid ESO, `https://api.carbonintensity.org.uk`, no key, 48 h forecast, 30 min). Fields: `region` (national or a regional id).
- New provider `energyCharts` (Fraunhofer ISE, `https://api.energy-charts.info/signal`, no key, CC BY 4.0). Field: `country` (16 European countries with data, checked 2026-10-18). The value is `100 - renewable share`, in percent, so lower is cleaner. The forecast reaches today or tomorrow. The API has no history, so there is no profile.
- Keep `electricityMaps` as an optional paid source.
- Order of preference per region: real carbon forecast (`ukCarbonIntensity`, `energiDataService` CO2, `electricityMaps`), then `energyCharts`, then `weatherProxy`. Users can combine them with weights, for example `energyCharts` for the next hours and `weatherProxy` for later days.

### 8.3 Argo Workflows

offpeak needs no special code for Argo Workflows. A workflow step uses an Argo `resource` template:

```yaml
- name: train
  resource:
    action: create
    setOwnerReference: true
    successCondition: status.phase == Succeeded
    failureCondition: status.phase == Failed
    manifest: |
      apiVersion: offpeak.thinnesen.com/v1alpha1
      kind: DeferrableJob
      metadata:
        generateName: train-
      spec: { ... }
```

- The workflow service account needs `create`, `get`, `list`, `watch` and `patch` on `deferrablejobs` in its namespace. The README gives a Role.
- `setOwnerReference: true`: when the workflow is deleted, the DeferrableJob and its Job are deleted.
- `generateName` gives each workflow run its own DeferrableJob.
- The e2e suite installs Argo Workflows (pinned version and digest) and runs one workflow with a short window.
- Later, not now: a DeferrableJob that starts a whole `WorkflowTemplate` instead of a Job.

## 9. Not in v1alpha1

Do not build these now. Add them to the backlog as "later" only.

- Price-aware Karpenter NodePool weights or disruption budgets.
- A CronJob-like recurring schedule (`DeferrableCronJob`).
- Preemption or pause/resume of running Jobs.
- Energy models per instance type (watts). The objective uses mean intensity, which is proportional to the emissions for a fixed job.
