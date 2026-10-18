# Hidden setup for the VHS tapes. It writes the demo manifests with deadlines relative to now.
export PS1='\[\e[38;5;141m\]❯\[\e[0m\] '
mkdir -p /tmp/demo && cd /tmp/demo || exit 1
ts() { date -u -d "$1" +%Y-%m-%dT%H:%M:%SZ; }

DK_SIGNALS='    - sourceRef: dk1-carbon          # Energi Data Service CO2 prognosis
      weight: 70
    - sourceRef: eu-north-1-weather  # Open-Meteo sun and wind near the region
      weight: 30'
OREGON_SIGNALS='    - sourceRef: us-west-2-weather   # sun and wind near AWS us-west-2 (Open-Meteo)
      weight: 100'

job() { # name deadline duration signals [karpenter block]
cat <<YAML
apiVersion: offpeak.thinnesen.com/v1alpha1
kind: DeferrableJob
metadata:
  name: $1
spec:
  deadline: "$(ts "$2")"
  estimatedDuration: $3
  signals:
$4
$5  jobTemplate:
    spec:
      template:
        spec:
          restartPolicy: Never
          containers:
            - name: train
              image: busybox:1.38.0
              command: ["sh", "-c", "echo training; sleep 20"]
YAML
}

job train-model "+30 hours" 2h "$OREGON_SIGNALS" > train-model.yaml
job nightly-report "+3 minutes" 2m "$DK_SIGNALS" > nightly-report.yaml
job gpu-batch "+4 minutes" 1m "$DK_SIGNALS" "  karpenter:
    nodePool: demo
    capacityType: SpotWithFallback   # try spot first, on-demand before the deadline
    fallbackMargin: 1m
" > gpu-batch.yaml
kubectl delete djob --all --wait=false >/dev/null 2>&1
# Warm up: a real plan with every source, so that each SignalSource has a status.
ALL_SIGNALS="$DK_SIGNALS
    - sourceRef: us-west-2-weather
      weight: 10"
job warmup "+30 hours" 1h "$ALL_SIGNALS" | kubectl apply -f - >/dev/null
until kubectl get djob warmup -o jsonpath='{.status.phase}' 2>/dev/null | grep -q .; do sleep 1; done
kubectl delete djob warmup --wait=false >/dev/null
clear
