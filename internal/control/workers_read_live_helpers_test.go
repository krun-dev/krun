package control

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/krun-dev/krun/internal/adapters/objectstore"
	"github.com/krun-dev/krun/internal/adapters/runtimebundle"
	"github.com/krun-dev/krun/internal/config"
	"github.com/krun-dev/krun/internal/domain"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

func startLiveReadRuntime(t *testing.T, ctx context.Context, c *config.WorkersConfig, backend string, validator bool) (string, string) {
	t.Helper()
	pool := c.RuntimePools[0]
	text, err := runtimebundle.Render(c, pool, backend, []string{"0.0.0.0/0", "::/0"}, validator)
	if err != nil {
		t.Fatal("runtime render failed")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	for name, value := range map[string]string{
		"pod/name": "live-test", "pod/uid": "live-test",
		"secrets/KRUN_RUNTIME_TOKEN":    pool.RuntimeToken,
		"secrets/KRUN_MANAGEMENT_TOKEN": pool.ManagementToken,
		"storage/access":                c.S3AccessKey, "storage/secret": c.S3SecretKey,
	} {
		filename := filepath.Join(directory, name)
		if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
			t.Fatal("runtime private directory failed")
		}
		if err := os.WriteFile(filename, []byte(value), 0600); err != nil {
			t.Fatal("runtime private binding failed")
		}
		text = strings.ReplaceAll(text, `"/`+name+`"`, quoteLiveRead(filename))
	}
	text = strings.ReplaceAll(text, `"/app/`, `"`+filepath.ToSlash(filepath.Join(root, "workers/runtime"))+`/`)
	filename := filepath.Join(directory, "runtime.capnp")
	if err := os.WriteFile(filename, []byte(text), 0600); err != nil {
		t.Fatal("runtime file failed")
	}
	freeAddress := func() string {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		address := listener.Addr().String()
		listener.Close()
		return address
	}
	business, management := freeAddress(), freeAddress()
	args := []string{"serve", filename, "-I", "/", "--experimental", "-s", "management=" + management}
	if !validator {
		args = append(args, "-s", "private="+business)
	}
	childCtx, stop := context.WithCancel(ctx)
	command := exec.CommandContext(childCtx, filepath.Join(root, "workers/runtime/node_modules/.bin/workerd"), args...)
	log, err := os.OpenFile(filepath.Join(directory, "workerd.log"), os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	command.Stdout, command.Stderr = log, log
	if err := command.Start(); err != nil {
		log.Close()
		stop()
		t.Fatal("real workerd could not start")
	}
	t.Cleanup(func() { stop(); _ = command.Wait(); log.Close() })
	client := &http.Client{Timeout: 10 * time.Second}
	for attempt := 0; attempt < 50; attempt++ {
		response, err := client.Get("http://" + management + "/ready")
		if err == nil {
			response.Body.Close()
			if response.StatusCode == 200 {
				return "http://" + business, "http://" + management
			}
		}
		select {
		case <-ctx.Done():
			t.Fatal("runtime readiness timed out")
		case <-time.After(100 * time.Millisecond):
		}
	}
	t.Fatal("real workerd did not become ready; private diagnostics retained until test cleanup")
	return "", ""
}

func quoteLiveRead(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func validateLiveReadCandidate(t *testing.T, ctx context.Context, endpoint, token string, payload any) {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal("candidate encoding failed")
	}
	request, _ := http.NewRequestWithContext(ctx, "POST", endpoint+"/validate", bytes.NewReader(raw))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: 30 * time.Second}).Do(request)
	if err != nil {
		t.Fatal("candidate validation request failed")
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode != 200 {
		t.Fatalf("candidate validation returned %d", response.StatusCode)
	}
}

func verifyLiveReadRestrictions(t *testing.T, ctx context.Context, c *config.WorkersConfig, grant domain.StorageReadGrant, artifactKey string) {
	t.Helper()
	endpoint, err := url.Parse(grant.Endpoint)
	if err != nil {
		t.Fatal("read endpoint invalid")
	}
	client, err := minio.New(endpoint.Host, &minio.Options{
		Creds:  credentials.NewStaticV4(grant.Credentials.AccessKeyID, grant.Credentials.SecretAccessKey, grant.Credentials.SessionToken),
		Secure: endpoint.Scheme == "https", Region: grant.Region, BucketLookup: minio.BucketLookupDNS,
	})
	if err != nil {
		t.Fatal("temporary read client failed")
	}
	object, err := client.GetObject(ctx, grant.Bucket, grant.Prefix+"bundle.tar.gz", minio.GetObjectOptions{})
	if err != nil {
		t.Fatal("scoped archive read failed")
	}
	_, err = io.Copy(io.Discard, object)
	object.Close()
	if err != nil {
		t.Fatal("scoped archive read failed")
	}
	denied := func(operation string, err error) {
		t.Helper()
		response := minio.ToErrorResponse(err)
		if response.StatusCode != 403 || response.Code != "AccessDenied" {
			t.Fatalf("%s was not denied by the real STS policy: %s %d", operation, response.Code, response.StatusCode)
		}
	}
	blockedKey := grant.Prefix + "blocked.txt"
	writer, err := objectstore.NewWorkerS3(c)
	if err != nil {
		t.Fatal("fixture cleanup client failed")
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		relative := strings.TrimPrefix(blockedKey, c.S3Prefix+"/")
		if err := writer.RemoveObject(cleanup, c.S3Bucket, relative, objectstore.RemoveObjectOptions{}); err != nil {
			t.Error("blocked-write fixture cleanup failed")
		}
		if _, err := writer.StatObject(cleanup, c.S3Bucket, relative, objectstore.StatObjectOptions{}); objectstore.ToErrorResponse(err).StatusCode != 404 {
			t.Error("blocked-write fixture absence unconfirmed")
		}
	})
	_, err = client.PutObject(ctx, grant.Bucket, blockedKey, strings.NewReader("blocked"), 7, minio.PutObjectOptions{DisableMultipart: true})
	denied("PUT", err)
	denied("DELETE", client.RemoveObject(ctx, grant.Bucket, grant.Prefix+"bundle.tar.gz", minio.RemoveObjectOptions{}))
	var listError error
	for object := range client.ListObjects(ctx, grant.Bucket, minio.ListObjectsOptions{Prefix: grant.Prefix, MaxKeys: 1}) {
		listError = object.Err
		break
	}
	denied("LIST", listError)
	digest := strings.Split(artifactKey, "/")[3]
	_, err = client.StatObject(ctx, grant.Bucket, c.S3Key("int/org-a/svc-a/"+digest+"/index.html"), minio.StatObjectOptions{})
	denied("adjacent asset read", err)
}
