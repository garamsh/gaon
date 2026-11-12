package api

import (
	"fmt"
	"log/slog"
)

// MaxDataBytes is the most keys and values of a Secret or a ConfigMap hold
// together. It is below Kubernetes' 1 MiB because JSON carries a Secret's
// bytes in base64, a third larger, and the Client reads at most 1 MiB of an
// answer.
const MaxDataBytes = 512 * 1024

// Secret is a map of sensitive values a MicroVM references by key, as an
// environment variable or the content of a file. It has no spec and no
// status. Its values are never written to a log or an Event: a Secret formats
// and logs as its name, and an error about one names a key and never a value.
type Secret struct {
	TypeMeta
	Metadata ObjectMeta        `json:"metadata"`
	Data     map[string][]byte `json:"data,omitempty"`
}

// SecretList is the answer to listing Secrets.
type SecretList struct {
	TypeMeta
	Metadata ListMeta `json:"metadata"`
	Items    []Secret `json:"items"`
}

// String formats s as its name, without its values.
func (s Secret) String() string { return fmt.Sprintf("Secret %q", s.Metadata.Name) }

// GoString formats s as its name, without its values.
func (s Secret) GoString() string { return s.String() }

// LogValue logs s as its name, without its values.
func (s Secret) LogValue() slog.Value { return slog.StringValue(s.String()) }

// Validate checks the identity, the name and the metadata of s, and its keys
// and size.
func (s *Secret) Validate() error {
	if err := validateIdentity(s.TypeMeta, GroupVersion, KindSecret, s.Metadata); err != nil {
		return err
	}
	if err := validateObjectName("metadata.name", s.Metadata.Name); err != nil {
		return err
	}
	if err := s.Metadata.Validate(); err != nil {
		return err
	}
	return validateData(s.Data)
}
