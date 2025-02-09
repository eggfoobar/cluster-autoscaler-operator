package clusterautoscaler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	autoscalingv1 "github.com/openshift/cluster-autoscaler-operator/pkg/apis/autoscaling/v1"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/klog/v2"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// Validator validates ClusterAutoscaler resources.
type Validator struct {
	client  client.Client
	decoder *admission.Decoder

	clusterAutoscalerName string
}

// NewValidator returns a new Validator configured with the given
// ClusterAutoscaler singleton resource name.
func NewValidator(name string) *Validator {
	return &Validator{
		clusterAutoscalerName: name,
	}
}

// Validate validates the given ClusterAutoscaler resource and returns a bool
// indicating whether validation passed, and possibly an aggregate error
// representing any validation errors found.
func (v *Validator) Validate(ca *autoscalingv1.ClusterAutoscaler) (bool, utilerrors.Aggregate) {
	var errs []error

	if ca == nil {
		err := errors.New("ClusterAutoscaler is nil")
		return false, utilerrors.NewAggregate([]error{err})
	}

	if ca.GetName() != v.clusterAutoscalerName {
		errs = append(errs, fmt.Errorf("Name %q is invalid, only %q is allowed",
			ca.GetName(), v.clusterAutoscalerName))
	}

	if limits := ca.Spec.ResourceLimits; limits != nil {
		if aggErr := v.validateResourceLimits(limits); aggErr != nil {
			errs = append(errs, aggErr.Errors()...)
		}
	}

	if scaleDown := ca.Spec.ScaleDown; scaleDown != nil {
		if aggErr := v.validateScaleDownConfig(scaleDown); aggErr != nil {
			errs = append(errs, aggErr.Errors()...)
		}
	}

	if len(errs) > 0 {
		return false, utilerrors.NewAggregate(errs)
	}

	return true, nil
}

// validateResourceLimits validates ResourceLimits objects.
func (v *Validator) validateResourceLimits(rl *autoscalingv1.ResourceLimits) utilerrors.Aggregate {
	var errs []error

	if rl.MaxNodesTotal != nil && *rl.MaxNodesTotal < 0 {
		errs = append(errs,
			errors.New("ResourceLimits.MaxNodesTotal must be greater than 0"))
	}

	if rl.Cores != nil {
		if coresErrs := v.validateResourceRange(rl.Cores); coresErrs != nil {
			errs = append(errs, fmt.Errorf("ResourceLimits.Cores: %v", coresErrs))
		}
	}

	if rl.Memory != nil {
		if memErrs := v.validateResourceRange(rl.Memory); memErrs != nil {
			errs = append(errs, fmt.Errorf("ResourceLimits.Memory: %v", memErrs))
		}
	}

	for _, gpu := range rl.GPUS {
		// Construct a ResourceRange from the GPULimit so we can reuse the
		// validation logic.  GPULimit is just a ResourceRange with a type.
		rr := &autoscalingv1.ResourceRange{Min: gpu.Min, Max: gpu.Max}

		if gpuErrs := v.validateResourceRange(rr); gpuErrs != nil {
			errs = append(errs, fmt.Errorf("ResourceLimits.GPUS.%s: %v",
				gpu.Type, gpuErrs))
		}
	}

	return utilerrors.NewAggregate(errs)
}

// validateResourceRange validates ResourceRange objects.
func (v *Validator) validateResourceRange(rr *autoscalingv1.ResourceRange) utilerrors.Aggregate {
	var errs []error

	if rr.Min < 0 || rr.Max < 0 {
		errs = append(errs, errors.New("Min and Max must be greater than zero"))
	}

	if rr.Max < rr.Min {
		errs = append(errs, errors.New("Max must be greater than or equal to Min"))
	}

	return utilerrors.NewAggregate(errs)
}

// validateScaleDownConfig validates ScaleDownConfig objects.
func (v *Validator) validateScaleDownConfig(sd *autoscalingv1.ScaleDownConfig) utilerrors.Aggregate {
	var errs []error

	durations := map[string]*string{
		"DelayAfterAdd":     sd.DelayAfterAdd,
		"DelayAfterDelete":  sd.DelayAfterDelete,
		"DelayAfterFailure": sd.DelayAfterFailure,
		"UnneededTime":      sd.UnneededTime,
	}

	for name, durationString := range durations {
		if durationString == nil {
			continue
		}

		if _, err := time.ParseDuration(*durationString); err != nil {
			errs = append(errs, fmt.Errorf("ScaleDown.%s: %v", name, err))
		}
	}
	if sd.UtilizationThreshold != nil {
		utilizationThreshold, err := strconv.ParseFloat(*sd.UtilizationThreshold, 64)
		if err != nil {
			errs = append(errs, errors.New("ScaleDown.UtilizationThreshold must be a string representing float value."))
		}
		if utilizationThreshold <= float64(0) || utilizationThreshold >= float64(1) {
			errs = append(errs, errors.New("ScaleDown.UtilizationThreshold must be a value between 0 and 1."))
		}
	}

	return utilerrors.NewAggregate(errs)
}

// Handle handles HTTP requests for admission webhook servers.
func (v *Validator) Handle(ctx context.Context, req admission.Request) admission.Response {
	ca := &autoscalingv1.ClusterAutoscaler{}

	if err := v.decoder.Decode(req, ca); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}

	klog.Infof("Validation webhook called for ClustAutoscaler: %s", ca.GetName())

	if ok, err := v.Validate(ca); !ok {
		return admission.Denied(err.Error())
	}

	return admission.Allowed("ClusterAutoscaler valid")
}

// InjectClient injects the client.
func (v *Validator) InjectClient(c client.Client) error {
	v.client = c
	return nil
}

// InjectDecoder injects the decoder.
func (v *Validator) InjectDecoder(d *admission.Decoder) error {
	v.decoder = d
	return nil
}
