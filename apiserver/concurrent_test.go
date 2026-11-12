package apiserver_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/garamsh/gaon/lib/api"
)

func TestPutMicroVM_ConcurrentWritersOfOneVersionHaveOneWinner(t *testing.T) {
	e := newEnv(t)
	m := e.createMicroVM(t, microvm("m1"))

	const writers = 8
	errs := make(chan error, writers)
	for i := 0; i < writers; i++ {
		go func() {
			update := *m
			update.Spec.IdleTimeoutSeconds = int32(i + 1)
			_, err := e.api.PutMicroVM(ctx(t), &update)
			errs <- err
		}()
	}
	won := 0
	for i := 0; i < writers; i++ {
		if err := <-errs; err == nil {
			won++
		} else {
			assert.ErrorIs(t, err, api.ErrConflict)
		}
	}
	assert.Equal(t, 1, won)
}
