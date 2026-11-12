package microvms

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/mvmd/lib/runtimeapi/v1alpha2"
)

const (
	// TokenPath is where the token of a MicroVM's ServiceAccount is a file in
	// the guest.
	TokenPath = "/var/run/secrets/gaon.garam.sh/serviceaccount/token"
	// tokenMode lets the file's owner read it and no one else.
	tokenMode = 0o600
)

// tokenState is what the agent knows of the token file in the guest, on its
// own clock. A zero renewAt means the file holds no token the agent has
// renewed since mvmd sent it.
type tokenState struct {
	// renewAt is the time after which a new token is requested: 80% of the
	// lifetime requested has passed.
	renewAt time.Time
	// expiresAt is when the token in the file expires.
	expiresAt time.Time
	// createdExpiresAt is when the token mvmd sends at every boot expires.
	createdExpiresAt time.Time
}

// withToken puts the token file of the MicroVM's ServiceAccount in req, when
// it names one. total is the size of the files already in req. A file the
// spec puts at TokenPath, a user that is not a numeric uid:gid, a file count
// or size over the bound, and a ServiceAccount that does not exist are
// returned as a specFailure.
func (s *service) withToken(ctx context.Context, m *api.MicroVM, req *v1alpha2.CreateMachineRequest, total int) (tokenState, error) {
	if m.Spec.ServiceAccountName == "" {
		return tokenState{}, nil
	}
	if slices.ContainsFunc(m.Spec.Files, func(f api.MicroVMFile) bool { return f.Path == TokenPath }) {
		return tokenState{}, specFailure{ReasonInvalidSpec, fmt.Errorf("spec.files has a file at %s, where the agent puts the ServiceAccount token", TokenPath)}
	}
	uid, gid, err := fileOwner(&m.Spec, api.MicroVMFile{Path: TokenPath})
	if err != nil {
		return tokenState{}, err
	}
	token, state, err := s.requestToken(ctx, m)
	if err != nil {
		return tokenState{}, err
	}
	if len(req.Files) >= api.MaxFiles {
		return tokenState{}, specFailure{ReasonFilesOverBound, fmt.Errorf("spec.files and the ServiceAccount token make more than %d files", api.MaxFiles)}
	}
	if total+len(token) > api.MaxFilesBytes {
		return tokenState{}, specFailure{ReasonFilesOverBound, fmt.Errorf("the content of spec.files and the ServiceAccount token is over %d bytes", api.MaxFilesBytes)}
	}
	req.Files = append(req.Files, &v1alpha2.File{Path: TokenPath, Content: token, Mode: tokenMode, Uid: uid, Gid: gid})
	state.createdExpiresAt = state.expiresAt
	return state, nil
}

// requestToken asks the API server for a token of the MicroVM's
// ServiceAccount bound to the MicroVM. The state is on the agent's clock at
// the request, not on the server's, so the two cannot disagree on when the
// token expires. A ServiceAccount that does not exist is a specFailure; any
// other error is retried. The token is never put in an error.
func (s *service) requestToken(ctx context.Context, m *api.MicroVM) ([]byte, tokenState, error) {
	name := m.Spec.ServiceAccountName
	if s.params.TokenAudience == "" {
		return nil, tokenState{}, fmt.Errorf("request a token of ServiceAccount %q: the agent is given no token audience (--service-account-audience)", name)
	}
	req := &api.TokenRequest{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindTokenRequest},
		Spec: api.TokenRequestSpec{
			Audiences:    []string{s.params.TokenAudience},
			BoundMicroVM: api.BoundObject{Name: m.Metadata.Name, UID: m.Metadata.UID},
		},
	}
	at := s.clock.Now()
	got, err := s.api.CreateServiceAccountToken(ctx, name, req)
	switch {
	case errors.Is(err, api.ErrNotFound):
		return nil, tokenState{}, specFailure{ReasonReferenceNotFound, fmt.Errorf("ServiceAccount %q not found", name)}
	case err != nil:
		return nil, tokenState{}, fmt.Errorf("request a token of ServiceAccount %q: %w", name, err)
	case got.Status.Token == "":
		return nil, tokenState{}, fmt.Errorf("the token of ServiceAccount %q was issued empty", name)
	}
	lifetime := req.Spec.Lifetime()
	return []byte(got.Status.Token), tokenState{renewAt: at.Add(lifetime / 10 * 8), expiresAt: at.Add(lifetime)}, nil
}
