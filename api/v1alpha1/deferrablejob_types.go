package v1alpha1

import (
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// DeferrableJobPhase is the phase of a DeferrableJob.
// +kubebuilder:validation:Enum=Waiting;Running;Succeeded;Failed
type DeferrableJobPhase string

const (
	PhaseWaiting   DeferrableJobPhase = "Waiting"
	PhaseRunning   DeferrableJobPhase = "Running"
	PhaseSucceeded DeferrableJobPhase = "Succeeded"
	PhaseFailed    DeferrableJobPhase = "Failed"
)

// Condition types of a DeferrableJob.
const (
	// ConditionPlanned is true when the status holds a valid plan.
	ConditionPlanned = "Planned"
	// ConditionSignalsAvailable is false when one or more signals failed.
	ConditionSignalsAvailable = "SignalsAvailable"
	// ConditionDeadlineAtRisk is true when the Job cannot finish before the deadline if it waits.
	ConditionDeadlineAtRisk = "DeadlineAtRisk"
	// ConditionKarpenterReady is false when the Karpenter NodePool is missing or not usable.
	ConditionKarpenterReady = "KarpenterReady"
	// ConditionJobCreated is true when the controller created or adopted the Job.
	ConditionJobCreated = "JobCreated"
	// ConditionFallback is true when offpeak replaced the spot Job with the on-demand fallback.
	ConditionFallback = "Fallback"
)

// LabelDeferrableJob is the label on the Job that holds the name of its DeferrableJob.
const LabelDeferrableJob = "offpeak.thinnesen.com/deferrable-job"

// DeferrableJobSpec defines when and what to run.
// +kubebuilder:validation:XValidation:rule="!has(self.notBefore) || timestamp(self.notBefore) < timestamp(self.deadline)",message="notBefore must be before deadline"
type DeferrableJobSpec struct {
	// deadline is the time by which the Job must finish.
	// +required
	Deadline metav1.Time `json:"deadline"`

	// notBefore is the earliest start time. The default is the creation time.
	// +optional
	NotBefore *metav1.Time `json:"notBefore,omitempty"`

	// estimatedDuration is the expected run time of the Job, including the node start-up time.
	// +kubebuilder:validation:XValidation:rule="duration(self) > duration('0s')",message="estimatedDuration must be positive"
	// +required
	EstimatedDuration metav1.Duration `json:"estimatedDuration"`

	// signals are the weighted signals that the planner minimizes.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=8
	// +listType=map
	// +listMapKey=sourceRef
	// +required
	Signals []SignalRef `json:"signals"`

	// karpenter connects the Job to a Karpenter NodePool.
	// +optional
	Karpenter *KarpenterSpec `json:"karpenter,omitempty"`

	// jobTemplate is the Job to create at the planned start.
	// +required
	JobTemplate batchv1.JobTemplateSpec `json:"jobTemplate"`
}

// SignalRef refers to a SignalSource and gives it a weight.
type SignalRef struct {
	// sourceRef is the name of a SignalSource.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`
	// +required
	SourceRef string `json:"sourceRef"`

	// weight is the importance of the signal, from 1 to 100.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=100
	// +required
	Weight int32 `json:"weight"`
}

// CapacityType selects spot or on-demand capacity of the Karpenter NodePool.
// +kubebuilder:validation:Enum=Any;Spot;OnDemand;SpotWithFallback
type CapacityType string

const (
	// CapacityAny does not change the capacity type. Karpenter decides.
	CapacityAny CapacityType = "Any"
	// CapacitySpot runs the Job on spot capacity only.
	CapacitySpot CapacityType = "Spot"
	// CapacityOnDemand runs the Job on on-demand capacity only.
	CapacityOnDemand CapacityType = "OnDemand"
	// CapacitySpotWithFallback runs on spot first and on on-demand in time for the deadline.
	CapacitySpotWithFallback CapacityType = "SpotWithFallback"
)

// KarpenterSpec selects a Karpenter NodePool.
type KarpenterSpec struct {
	// nodePool is the name of the Karpenter NodePool for the Job.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`
	// +required
	NodePool string `json:"nodePool"`

	// instanceTypes limit the Job to these EC2 instance types. The default is the NodePool requirement.
	// +kubebuilder:validation:MaxItems=50
	// +kubebuilder:validation:items:MaxLength=64
	// +kubebuilder:validation:items:Pattern=`^[a-z0-9-]+\.[a-z0-9-]+$`
	// +listType=set
	// +optional
	InstanceTypes []string `json:"instanceTypes,omitempty"`

	// capacityType is Any (the default), Spot, OnDemand or SpotWithFallback.
	// +kubebuilder:default=Any
	// +optional
	CapacityType CapacityType `json:"capacityType,omitempty"`

	// fallbackMargin is the safety time for the on-demand fallback. The default is 15m.
	// +kubebuilder:validation:XValidation:rule="duration(self) >= duration('0s')",message="fallbackMargin must not be negative"
	// +optional
	FallbackMargin *metav1.Duration `json:"fallbackMargin,omitempty"`
}

// AttemptResult is the result of one Job attempt.
// +kubebuilder:validation:Enum=Running;Succeeded;Failed;Replaced
type AttemptResult string

const (
	AttemptRunning   AttemptResult = "Running"
	AttemptSucceeded AttemptResult = "Succeeded"
	AttemptFailed    AttemptResult = "Failed"
	// AttemptReplaced means that offpeak deleted the spot Job and started the on-demand fallback.
	AttemptReplaced AttemptResult = "Replaced"
)

// JobAttempt is one Job that offpeak created for the DeferrableJob.
type JobAttempt struct {
	// jobName is the name of the Job.
	// +required
	JobName string `json:"jobName"`

	// capacityType is the capacity type of the Job: Any, Spot or OnDemand.
	// +optional
	CapacityType CapacityType `json:"capacityType,omitempty"`

	// startedAt is the time when offpeak created the Job.
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`

	// finishedAt is the time when the attempt ended.
	// +optional
	FinishedAt *metav1.Time `json:"finishedAt,omitempty"`

	// result is Running, Succeeded, Failed or Replaced.
	// +optional
	Result AttemptResult `json:"result,omitempty"`
}

// SignalEstimate is the expected mean of one signal over the run window.
type SignalEstimate struct {
	// source is the name of the SignalSource.
	// +required
	Source string `json:"source"`

	// unit is the unit of the values.
	// +optional
	Unit string `json:"unit,omitempty"`

	// planned is the mean over the planned run window, as a decimal string.
	// +optional
	Planned string `json:"planned,omitempty"`

	// ifStartedNow is the mean over the run window that starts at the earliest time.
	// +optional
	IfStartedNow string `json:"ifStartedNow,omitempty"`

	// assumedPercent is the part of the planned run window without real data.
	// +optional
	AssumedPercent int32 `json:"assumedPercent,omitempty"`
}

// DeferrableJobStatus defines the observed state of DeferrableJob.
type DeferrableJobStatus struct {
	// phase is Waiting, Running, Succeeded or Failed.
	// +optional
	Phase DeferrableJobPhase `json:"phase,omitempty"`

	// plannedStart is the planned start time of the Job.
	// +optional
	PlannedStart *metav1.Time `json:"plannedStart,omitempty"`

	// latestStart is the last start time that meets the deadline.
	// +optional
	LatestStart *metav1.Time `json:"latestStart,omitempty"`

	// lastPlanTime is the time of the last plan.
	// +optional
	LastPlanTime *metav1.Time `json:"lastPlanTime,omitempty"`

	// estimates hold one entry for each signal with data.
	// +listType=map
	// +listMapKey=source
	// +optional
	Estimates []SignalEstimate `json:"estimates,omitempty"`

	// savingsPercent is the weighted saving of the plan against a start now.
	// +optional
	SavingsPercent *int32 `json:"savingsPercent,omitempty"`

	// jobName is the name of the created Job.
	// +optional
	JobName string `json:"jobName,omitempty"`

	// startedAt is the time when the controller created the Job.
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`

	// attempts hold one entry for each Job that offpeak created. SpotWithFallback can have two.
	// +listType=map
	// +listMapKey=jobName
	// +optional
	Attempts []JobAttempt `json:"attempts,omitempty"`

	// conditions hold Planned, SignalsAvailable, DeadlineAtRisk, KarpenterReady and JobCreated.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=djob
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Planned start",type=string,JSONPath=".status.plannedStart"
// +kubebuilder:printcolumn:name="Deadline",type=string,JSONPath=".spec.deadline"
// +kubebuilder:printcolumn:name="Savings %",type=integer,JSONPath=".status.savingsPercent"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// DeferrableJob runs a Job in the best window before a deadline.
// +kubebuilder:validation:XValidation:rule="!has(self.spec.karpenter) || !has(self.spec.karpenter.capacityType) || self.spec.karpenter.capacityType != 'SpotWithFallback' || size(self.metadata.name) <= 60",message="with SpotWithFallback, the name must have at most 60 characters"
type DeferrableJob struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines when and what to run.
	// +required
	Spec DeferrableJobSpec `json:"spec"`

	// status defines the observed state of DeferrableJob
	// +optional
	Status DeferrableJobStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// DeferrableJobList contains a list of DeferrableJob
type DeferrableJobList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []DeferrableJob `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &DeferrableJob{}, &DeferrableJobList{})
		return nil
	})
}
