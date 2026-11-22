package executor

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
)

// PriorityClassName is copied into the spec of every step pod, so that under memory pressure the node
// evicts builds before services: give it a PriorityClass with a low value. Empty: the pod has none and
// nothing changes. checkPriorityClass refuses a name Kubernetes would not accept (a DNS subdomain).
func (o KubernetesOptions) checkPriorityClass() error {
	if o.PriorityClassName == "" {
		return nil
	}
	if errs := validation.IsDNS1123Subdomain(o.PriorityClassName); len(errs) > 0 {
		return fmt.Errorf("kubernetes: priority_class_name %q is not a name Kubernetes accepts (a DNS subdomain: lower-case letters, digits, '-' and '.'): %s",
			o.PriorityClassName, strings.Join(errs, "; "))
	}
	return nil
}
