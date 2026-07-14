package gmail

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type recordingTransport struct {
	calls int
}

func (t *recordingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	t.calls++
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader("ok")),
		Header:     make(http.Header),
	}, nil
}

func TestReadOnlyTransportAllowsOnlyGetAndHead(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		t.Run(method, func(t *testing.T) {
			base := &recordingTransport{}
			transport := &readOnlyTransport{base: base}
			req, err := http.NewRequestWithContext(context.Background(), method, "https://gmail.googleapis.com/gmail/v1/users/me/profile", nil)
			require.NoError(t, err)

			resp, err := transport.RoundTrip(req)
			require.NoError(t, err)
			require.NoError(t, resp.Body.Close())
			assert.Equal(t, 1, base.calls)
		})
	}
}

func TestReadOnlyTransportRejectsMutationMethodsWithoutSending(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			base := &recordingTransport{}
			transport := &readOnlyTransport{base: base}
			req, err := http.NewRequestWithContext(context.Background(), method, "https://gmail.googleapis.com/gmail/v1/users/me/messages/1", nil)
			require.NoError(t, err)

			resp, err := transport.RoundTrip(req)
			if resp != nil {
				require.NoError(t, resp.Body.Close())
			}
			require.Error(t, err)
			require.ErrorContains(t, err, "read-only transport")
			assert.Zero(t, base.calls)
		})
	}
}

func TestClientRequestRejectsMutationBeforeRateLimitOrNetwork(t *testing.T) {
	client := &Client{rateLimiter: NewRateLimiter(1), logger: slog.Default()}
	_, err := client.request(context.Background(), OpMessagesGet, http.MethodPost, "/users/me/messages/1/trash")
	require.Error(t, err)
	assert.ErrorContains(t, err, "read-only transport")
}
