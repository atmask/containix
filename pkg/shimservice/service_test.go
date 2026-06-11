package shimservice

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	taskAPI "github.com/containerd/containerd/api/runtime/task/v3"
	"github.com/containerd/ttrpc"
	ocispec "github.com/opencontainers/runtime-spec/specs-go"
	"google.golang.org/protobuf/types/known/emptypb"
)

// mockTaskService implements taskAPI.TTRPCTaskService for use as the inner
// service in a Service wrapper test. It records whether Create was called
// and with what request. All other methods are no-ops that return zero
// values — they exist only to satisfy the interface.
type mockTaskService struct {
	createCalled bool
	createReq    *taskAPI.CreateTaskRequest
	createReturn *taskAPI.CreateTaskResponse
	createErr    error
}

func (m *mockTaskService) Create(ctx context.Context, r *taskAPI.CreateTaskRequest) (*taskAPI.CreateTaskResponse, error) {
	m.createCalled = true
	m.createReq = r
	if m.createReturn == nil {
		return &taskAPI.CreateTaskResponse{Pid: 42}, m.createErr
	}
	return m.createReturn, m.createErr
}

// The remaining 16 methods of TTRPCTaskService — no-op stubs.

func (m *mockTaskService) State(context.Context, *taskAPI.StateRequest) (*taskAPI.StateResponse, error) {
	return nil, nil
}
func (m *mockTaskService) Start(context.Context, *taskAPI.StartRequest) (*taskAPI.StartResponse, error) {
	return nil, nil
}
func (m *mockTaskService) Delete(context.Context, *taskAPI.DeleteRequest) (*taskAPI.DeleteResponse, error) {
	return nil, nil
}
func (m *mockTaskService) Pids(context.Context, *taskAPI.PidsRequest) (*taskAPI.PidsResponse, error) {
	return nil, nil
}
func (m *mockTaskService) Pause(context.Context, *taskAPI.PauseRequest) (*emptypb.Empty, error) {
	return nil, nil
}
func (m *mockTaskService) Resume(context.Context, *taskAPI.ResumeRequest) (*emptypb.Empty, error) {
	return nil, nil
}
func (m *mockTaskService) Checkpoint(context.Context, *taskAPI.CheckpointTaskRequest) (*emptypb.Empty, error) {
	return nil, nil
}
func (m *mockTaskService) Kill(context.Context, *taskAPI.KillRequest) (*emptypb.Empty, error) {
	return nil, nil
}
func (m *mockTaskService) Exec(context.Context, *taskAPI.ExecProcessRequest) (*emptypb.Empty, error) {
	return nil, nil
}
func (m *mockTaskService) ResizePty(context.Context, *taskAPI.ResizePtyRequest) (*emptypb.Empty, error) {
	return nil, nil
}
func (m *mockTaskService) CloseIO(context.Context, *taskAPI.CloseIORequest) (*emptypb.Empty, error) {
	return nil, nil
}
func (m *mockTaskService) Update(context.Context, *taskAPI.UpdateTaskRequest) (*emptypb.Empty, error) {
	return nil, nil
}
func (m *mockTaskService) Wait(context.Context, *taskAPI.WaitRequest) (*taskAPI.WaitResponse, error) {
	return nil, nil
}
func (m *mockTaskService) Stats(context.Context, *taskAPI.StatsRequest) (*taskAPI.StatsResponse, error) {
	return nil, nil
}
func (m *mockTaskService) Connect(context.Context, *taskAPI.ConnectRequest) (*taskAPI.ConnectResponse, error) {
	return nil, nil
}
func (m *mockTaskService) Shutdown(context.Context, *taskAPI.ShutdownRequest) (*emptypb.Empty, error) {
	return nil, nil
}

// writeBundle is a small helper for setting up a synthetic OCI bundle.
func writeBundle(t *testing.T, annotations map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	spec := ocispec.Spec{
		Process:     &ocispec.Process{Args: []string{"/bin/sh"}},
		Root:        &ocispec.Root{Path: "rootfs"},
		Annotations: annotations,
	}
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal spec: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), data, 0o644); err != nil {
		t.Fatalf("write config.json: %v", err)
	}
	return dir
}

// ---- Create branching tests (no Nix required) ----

func TestCreate_PassthroughWhenNoFlakeAnnotation(t *testing.T) {
	// A container with no containix.dev/flake annotation should pass through
	// to the inner Create unmodified. No Nix work should be attempted.
	mock := &mockTaskService{}
	svc := &Service{TTRPCTaskService: mock}

	bundle := writeBundle(t, map[string]string{
		"some.other/annotation": "value",
	})

	req := &taskAPI.CreateTaskRequest{ID: "test-1", Bundle: bundle}
	resp, err := svc.Create(context.Background(), req)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if resp == nil {
		t.Fatal("expected response from inner Create")
	}
	if !mock.createCalled {
		t.Error("inner Create was not called")
	}
	if mock.createReq != req {
		t.Error("inner Create was not called with the original request")
	}
}

func TestCreate_PassthroughForSandboxContainer(t *testing.T) {
	// Sandbox containers (the pause container) must pass through even if a
	// flake annotation is somehow present. The Nix logic only applies to
	// app containers.
	mock := &mockTaskService{}
	svc := &Service{TTRPCTaskService: mock}

	bundle := writeBundle(t, map[string]string{
		"io.kubernetes.cri.container-type": "sandbox",
		"containix.dev/flake":              "nixpkgs#hello", // would normally trigger Nix
	})

	req := &taskAPI.CreateTaskRequest{ID: "sandbox-1", Bundle: bundle}
	_, err := svc.Create(context.Background(), req)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !mock.createCalled {
		t.Error("inner Create was not called for sandbox container")
	}
}

func TestCreate_ErrorOnBadBundle(t *testing.T) {
	// If FromBundle fails (e.g., missing config.json), Create propagates the
	// error and never invokes the inner.
	mock := &mockTaskService{}
	svc := &Service{TTRPCTaskService: mock}

	req := &taskAPI.CreateTaskRequest{
		ID:     "broken-1",
		Bundle: "/nonexistent/path/does/not/exist",
	}
	_, err := svc.Create(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from FromBundle, got nil")
	}
	if mock.createCalled {
		t.Error("inner Create should NOT have been called on bundle error")
	}
}

// ---- Happy path (Nix required) ----

func TestCreate_HappyPathWithNix(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
	if _, err := exec.LookPath("nix"); err != nil {
		t.Skip("nix not available on PATH; skipping integration test")
	}

	mock := &mockTaskService{}
	svc := &Service{TTRPCTaskService: mock}

	bundle := writeBundle(t, map[string]string{
		"containix.dev/flake":      "nixpkgs#hello",
		"containix.dev/entrypoint": "bin/hello",
	})

	req := &taskAPI.CreateTaskRequest{ID: "happy-1", Bundle: bundle}
	_, err := svc.Create(context.Background(), req)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !mock.createCalled {
		t.Error("inner Create was not called after Nix prep")
	}

	// Verify the bundle's config.json was modified (mounts added, args set).
	data, err := os.ReadFile(filepath.Join(bundle, "config.json"))
	if err != nil {
		t.Fatalf("read modified config.json: %v", err)
	}
	var modified ocispec.Spec
	if err := json.Unmarshal(data, &modified); err != nil {
		t.Fatalf("unmarshal modified spec: %v", err)
	}
	if len(modified.Mounts) == 0 {
		t.Error("expected mounts to be added; got zero")
	}
	if len(modified.Process.Args) != 1 {
		t.Errorf("Process.Args = %v, expected single resolved entrypoint", modified.Process.Args)
	}
}

// ---- RegisterTTRPC ----

func TestRegisterTTRPC_RegistersWithoutPanic(t *testing.T) {
	// The contract: RegisterTTRPC registers *Service (the wrapper) onto the
	// TTRPC server so containerd's RPCs hit our Create override. The simplest
	// verifiable property is that the call doesn't panic and returns nil.
	//
	// (The deeper "registers the wrapper not the inner" property is verified
	// in production by the smoke test against a real shim daemon — if it
	// registered the inner, our Create log line would never appear.)
	mock := &mockTaskService{}
	svc := &Service{TTRPCTaskService: mock}

	server, err := ttrpc.NewServer()
	if err != nil {
		t.Fatalf("ttrpc.NewServer: %v", err)
	}

	if err := svc.RegisterTTRPC(server); err != nil {
		t.Fatalf("RegisterTTRPC: %v", err)
	}
}
