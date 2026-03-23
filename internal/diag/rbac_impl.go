package diag

import (
	"fmt"
	"strings"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type RBACResult struct {
	DangerousBindings []DangerousBinding
	ServiceAccounts   []SubjectBinding
	Users             []SubjectBinding
}

type DangerousBinding struct {
	Subject   string
	RoleName  string
	Namespace string
	Risk      string
}

type SubjectBinding struct {
	Name        string
	Namespace   string
	RoleName    string
	ClusterWide bool
}

func (e *Engine) RBACAudit(filterNS, filterSubject string) (*RBACResult, error) {
	result := &RBACResult{}
	ns := filterNS
	if ns == "" {
		ns = e.ns()
	}
	rbs, err := e.k8s.RbacV1().RoleBindings(ns).List(e.ctx, metav1.ListOptions{})
	if err == nil {
		for _, rb := range rbs.Items {
			for _, subject := range rb.Subjects {
				if filterSubject != "" && !strings.Contains(strings.ToLower(subject.Name), strings.ToLower(filterSubject)) {
					continue
				}
				binding := SubjectBinding{Name: subject.Name, Namespace: rb.Namespace, RoleName: rb.RoleRef.Name}
				if subject.Kind == "ServiceAccount" {
					result.ServiceAccounts = append(result.ServiceAccounts, binding)
				} else {
					result.Users = append(result.Users, binding)
				}
			}
		}
	}
	crbs, err := e.k8s.RbacV1().ClusterRoleBindings().List(e.ctx, metav1.ListOptions{})
	if err == nil {
		for _, crb := range crbs.Items {
			for _, subject := range crb.Subjects {
				if strings.HasPrefix(subject.Name, "system:") {
					continue
				}
				if filterSubject != "" && !strings.Contains(strings.ToLower(subject.Name), strings.ToLower(filterSubject)) {
					continue
				}
				binding := SubjectBinding{Name: subject.Name, Namespace: subject.Namespace, RoleName: crb.RoleRef.Name, ClusterWide: true}
				if subject.Kind == "ServiceAccount" {
					result.ServiceAccounts = append(result.ServiceAccounts, binding)
				} else {
					result.Users = append(result.Users, binding)
				}
			}
		}
	}
	roles, err := e.k8s.RbacV1().Roles(ns).List(e.ctx, metav1.ListOptions{})
	if err == nil {
		roleSubjects := map[string][]string{}
		if rbs != nil {
			for _, rb := range rbs.Items {
				for _, s := range rb.Subjects {
					roleSubjects[rb.RoleRef.Name] = append(roleSubjects[rb.RoleRef.Name], s.Name)
				}
			}
		}
		for _, role := range roles.Items {
			risk := checkPolicyRules(role.Rules)
			if risk == "" {
				continue
			}
			subjects := strings.Join(roleSubjects[role.Name], ", ")
			if subjects == "" {
				subjects = "(unbound)"
			}
			result.DangerousBindings = append(result.DangerousBindings, DangerousBinding{
				Subject: subjects, RoleName: role.Name, Namespace: role.Namespace, Risk: risk,
			})
		}
	}
	crs, err := e.k8s.RbacV1().ClusterRoles().List(e.ctx, metav1.ListOptions{})
	if err == nil {
		crSubjects := map[string][]string{}
		if crbs != nil {
			for _, crb := range crbs.Items {
				for _, s := range crb.Subjects {
					if !strings.HasPrefix(s.Name, "system:") {
						crSubjects[crb.RoleRef.Name] = append(crSubjects[crb.RoleRef.Name], s.Name)
					}
				}
			}
		}
		for _, cr := range crs.Items {
			if strings.HasPrefix(cr.Name, "system:") {
				continue
			}
			risk := checkPolicyRules(cr.Rules)
			if risk == "" {
				continue
			}
			subjects := crSubjects[cr.Name]
			if len(subjects) == 0 {
				continue
			}
			result.DangerousBindings = append(result.DangerousBindings, DangerousBinding{
				Subject: strings.Join(subjects, ", "), RoleName: cr.Name, Namespace: "cluster-wide", Risk: risk,
			})
		}
	}
	return result, nil
}

func checkPolicyRules(rules []rbacv1.PolicyRule) string {
	for _, rule := range rules {
		hasWildcardVerb := false
		hasDangerousVerb := false
		for _, v := range rule.Verbs {
			if v == "*" {
				hasWildcardVerb = true
			}
			for _, dv := range []string{"delete", "patch", "update", "create", "escalate", "bind"} {
				if v == dv {
					hasDangerousVerb = true
				}
			}
		}
		for _, r := range rule.Resources {
			if r == "*" && hasWildcardVerb {
				return "wildcard on all resources — effectively cluster-admin"
			}
			if r == "secrets" && hasDangerousVerb {
				return "can read/modify secrets — credential exposure risk"
			}
			if r == "pods/exec" {
				return "can exec into pods — container escape risk"
			}
			if (r == "clusterroles" || r == "clusterrolebindings") && hasDangerousVerb {
				return "can modify RBAC — privilege escalation risk"
			}
		}
	}
	return ""
}

var _ = fmt.Sprintf
var _ = metav1.ListOptions{}
