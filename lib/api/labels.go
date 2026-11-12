package api

import (
	"regexp"
	"strings"
)

// The limits of a label and an annotation, which are Kubernetes' (Labels and
// Selectors): a key is an optional DNS subdomain prefix and a name, a value
// has at most maxLabelValue characters, and annotations hold at most
// maxAnnotationsSize bytes of keys and values together.
const (
	maxLabelName       = 63
	maxLabelValue      = 63
	maxLabelPrefix     = 253
	maxAnnotationsSize = 256 * 1024
)

var (
	labelName   = regexp.MustCompile(`^[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?$`)
	labelPrefix = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)
)

// validateLabelKey checks a key of a label or an annotation.
func validateLabelKey(key string) error {
	prefix, name, hasPrefix := strings.Cut(key, "/")
	if !hasPrefix {
		name = key
	} else if len(prefix) > maxLabelPrefix || !labelPrefix.MatchString(prefix) {
		return invalid("key %q has a prefix that is not a DNS subdomain of at most %d characters", key, maxLabelPrefix)
	}
	if len(name) > maxLabelName || !labelName.MatchString(name) {
		return invalid("key %q has a name that is not 1 to %d alphanumerics, '-', '_' or '.', beginning and ending with an alphanumeric", key, maxLabelName)
	}
	return nil
}

// validateLabelValue checks a label value: empty, or name-shaped.
func validateLabelValue(key, value string) error {
	if value == "" {
		return nil
	}
	if len(value) > maxLabelValue || !labelName.MatchString(value) {
		return invalid("label %q has value %q, want at most %d alphanumerics, '-', '_' or '.', beginning and ending with an alphanumeric", key, value, maxLabelValue)
	}
	return nil
}

// ValidateLabels checks each key and value of labels.
func ValidateLabels(labels map[string]string) error {
	for k, v := range labels {
		if err := validateLabelKey(k); err != nil {
			return err
		}
		if err := validateLabelValue(k, v); err != nil {
			return err
		}
	}
	return nil
}

// ValidateAnnotations checks each key of annotations, and that keys and
// values together stay within 256 KiB. A value is any string.
func ValidateAnnotations(annotations map[string]string) error {
	size := 0
	for k, v := range annotations {
		if err := validateLabelKey(k); err != nil {
			return err
		}
		size += len(k) + len(v)
	}
	if size > maxAnnotationsSize {
		return invalid("annotations hold %d bytes, want at most %d", size, maxAnnotationsSize)
	}
	return nil
}
