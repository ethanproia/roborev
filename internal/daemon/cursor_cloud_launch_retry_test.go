package daemon

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/roborev/internal/agent"
	"go.kenn.io/roborev/internal/cursorcloud"
	"go.kenn.io/roborev/internal/storage"
)

const cursorCloudLaunchSHA = "0123456789abcdef0123456789abcdef01234567"

func TestCursorCloudLaunchHTTPErrorDoesNotRetryJob(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{name: "5xx", status: http.StatusBadGateway, body: `{"message":"bad gateway"}`},
		{name: "429", status: http.StatusTooManyRequests, body: `{"message":"too many requests"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var posts atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost && r.URL.Path == "/v0/agents" {
					posts.Add(1)
					w.WriteHeader(tc.status)
					_, _ = io.WriteString(w, tc.body)
					return
				}
				http.NotFound(w, r)
			}))
			t.Cleanup(srv.Close)

			err := reviewCursorCloud(t, srv.URL, nil)
			require.Error(t, err)
			assert.Equal(t, int32(1), posts.Load())
			assertLaunchNotRetried(t, err)
		})
	}
}

func TestCursorCloudLaunchAmbiguousCreateDoesNotRetryJob(t *testing.T) {
	t.Run("error after send", func(t *testing.T) {
		var posts atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost && r.URL.Path == "/v0/agents" {
				posts.Add(1)
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, `{"id":`)
				return
			}
			http.NotFound(w, r)
		}))
		t.Cleanup(srv.Close)

		err := reviewCursorCloud(t, srv.URL, nil)
		require.Error(t, err)
		assert.Equal(t, int32(1), posts.Load())
		assertLaunchNotRetried(t, err)
	})

	t.Run("timeout after send", func(t *testing.T) {
		var posts atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost && r.URL.Path == "/v0/agents" {
				posts.Add(1)
				// The POST has been read. Hold the response longer than the
				// client timeout, then return so the server can shut down.
				time.Sleep(300 * time.Millisecond)
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, `{"id":"bc_late","status":"CREATING"}`)
				return
			}
			http.NotFound(w, r)
		}))
		t.Cleanup(srv.Close)

		// Wall-clock wait: the injected HTTP client times out after the
		// request has been written. synctest cannot see that socket.
		hc := &http.Client{Timeout: 50 * time.Millisecond}
		err := reviewCursorCloud(t, srv.URL, hc)
		require.Error(t, err)
		assert.Equal(t, int32(1), posts.Load())
		assertLaunchNotRetried(t, err)
	})
}

func TestCursorCloudLaunchDialErrorMayRetry(t *testing.T) {
	err := reviewCursorCloud(t, "http://127.0.0.1:1", nil)
	require.Error(t, err)
	assert.False(t, agent.IsUnavailable(err))

	tc := newWorkerTestContext(t, 1)
	job := tc.createAndClaimJobWithAgent(t, "cursor-cloud-dial", testWorkerID, agent.CursorCloudHost)
	job.RepoPath = tc.TmpDir
	tc.Pool.failOrRetryAgentExecutionContext(context.Background(), testWorkerID, job, agent.CursorCloudHost, err)

	updated := tc.assertJobStatus(t, job.ID, storage.JobStatusQueued)
	assert.Equal(t, agent.CursorCloudHost, updated.Agent)
	retryCount, retryErr := tc.DB.GetJobRetryCount(job.ID)
	require.NoError(t, retryErr)
	assert.Equal(t, 1, retryCount)
}

func reviewCursorCloud(t *testing.T, base string, hc *http.Client) error {
	t.Helper()
	opts := []cursorcloud.Option{cursorcloud.WithBaseURL(base)}
	if hc != nil {
		opts = append(opts, cursorcloud.WithHTTPClient(hc))
	}
	cloud := &agent.CursorCloudAgent{
		Client:  cursorcloud.NewHTTPClient("cursor-test-secret", opts...),
		RepoURL: "https://github.com/acme/widgets.git",
	}
	_, err := cloud.Review(context.Background(), t.TempDir(), cursorCloudLaunchSHA, "review the change", nil)
	return err
}

func assertLaunchNotRetried(t *testing.T, launchErr error) {
	t.Helper()
	assert.True(t, agent.IsUnavailable(launchErr))
	tc := newWorkerTestContext(t, 1)
	job := tc.createAndClaimJobWithAgent(t, "cursor-cloud-launch", testWorkerID, agent.CursorCloudHost)
	job.RepoPath = tc.TmpDir
	tc.Pool.failOrRetryAgentExecutionContext(
		context.Background(), testWorkerID, job, agent.CursorCloudHost, launchErr,
	)
	updated := tc.assertJobStatus(t, job.ID, storage.JobStatusFailed)
	assert.Contains(t, updated.Error, "cursor-cloud: launch failed")
	retryCount, err := tc.DB.GetJobRetryCount(job.ID)
	require.NoError(t, err)
	assert.Zero(t, retryCount)
}
