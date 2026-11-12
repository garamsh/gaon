package api

// maxObjectName is the longest name of a Secret, a ConfigMap or a
// ServiceAccount, a DNS subdomain as in Kubernetes. The name of a
// ServiceAccount is part of a token's subject, so it holds no ':'.
const maxObjectName = 253

// validateObjectName checks that name is a lowercase DNS subdomain of at most
// 253 characters.
func validateObjectName(field, name string) error {
	if len(name) > maxObjectName || !labelPrefix.MatchString(name) {
		return invalid("%s is %q, want a lowercase DNS subdomain of at most %d characters", field, name, maxObjectName)
	}
	return nil
}
