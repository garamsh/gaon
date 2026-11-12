package api

import "regexp"

// maxDataKey is the longest key of a Secret or a ConfigMap, as in Kubernetes.
const maxDataKey = 253

var dataKey = regexp.MustCompile(`^[-._a-zA-Z0-9]+$`)

// validateDataKey checks a key of the data of a Secret or a ConfigMap:
// alphanumerics, '-', '_' and '.', other than "." and "..".
func validateDataKey(field, key string) error {
	if len(key) > maxDataKey || key == "." || key == ".." || !dataKey.MatchString(key) {
		return invalid("%s is %q, want 1 to %d alphanumerics, '-', '_' or '.', other than \".\" and \"..\"", field, key, maxDataKey)
	}
	return nil
}

// validateData checks the keys of data and that keys and values together stay
// within MaxDataBytes. A value is never named in the error.
func validateData[V ~string | ~[]byte](data map[string]V) error {
	size := 0
	for k, v := range data {
		if err := validateDataKey("data key", k); err != nil {
			return err
		}
		size += len(k) + len(v)
	}
	if size > MaxDataBytes {
		return invalid("data holds %d bytes, want at most %d", size, MaxDataBytes)
	}
	return nil
}
