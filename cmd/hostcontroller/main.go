package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/hostcontroller"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/webassets"
	workspacepostgres "github.com/CodeZen-Lizhi/zhixu/internal/workspace/adapter/postgres"
	workspaceapplication "github.com/CodeZen-Lizhi/zhixu/internal/workspace/application"
)

const (
	controllerReadHeaderTimeout = 5 * time.Second
	controllerReadTimeout       = 30 * time.Second
	controllerIdleTimeout       = 60 * time.Second
	controllerShutdownTimeout   = 15 * time.Second
	controllerReconcileTimeout  = 30 * time.Minute
	controllerReconcileInterval = 2 * time.Second
)

type workspaceReconciler interface {
	Reconcile(context.Context) error
}

type controllerConfig struct {
	address          string
	assetsDirectory  string
	stateDirectory   string
	composeFile      string
	environmentFile  string
	grantOverride    string
	bootstrapTokenFD int
	databaseURLFD    int
	secureCookie     bool
}

func main() { os.Exit(run(os.Args[1:])) }

func run(arguments []string) int {
	config, err := parseConfig(arguments)
	if err != nil {
		fmt.Fprintln(os.Stderr, "host controller configuration is invalid")
		return 1
	}
	if err := hostcontroller.ValidateStateDirectory(config.stateDirectory); err != nil {
		fmt.Fprintln(os.Stderr, "host controller state is unsafe")
		return 1
	}
	if err := validateAssetsDirectory(config.assetsDirectory, config.stateDirectory); err != nil {
		fmt.Fprintln(os.Stderr, "host controller web assets are unsafe")
		return 1
	}
	if err := validateInputFile(config.composeFile); err != nil {
		fmt.Fprintln(os.Stderr, "host controller Compose input is unsafe")
		return 1
	}
	if err := validateProtectedInputFile(config.environmentFile); err != nil {
		fmt.Fprintln(os.Stderr, "host controller environment input is unsafe")
		return 1
	}
	token, err := readBootstrapToken(config.bootstrapTokenFD)
	if err != nil {
		fmt.Fprintln(os.Stderr, "host controller bootstrap credential is invalid")
		return 1
	}
	databaseURL, err := readDatabaseURL(config.databaseURLFD)
	if err != nil {
		fmt.Fprintln(os.Stderr, "host controller database credential is invalid")
		return 1
	}
	databaseContext, cancelDatabase := context.WithTimeout(context.Background(), 10*time.Second)
	database, err := platformpostgres.Open(databaseContext, databaseURL, 4, 1)
	cancelDatabase()
	if err != nil {
		fmt.Fprintln(os.Stderr, "host controller database is unavailable")
		return 1
	}
	defer database.Close()
	pingContext, cancelPing := context.WithTimeout(context.Background(), 5*time.Second)
	err = database.Ping(pingContext)
	cancelPing()
	if err != nil {
		fmt.Fprintln(os.Stderr, "host controller database is unavailable")
		return 1
	}
	instanceID, err := foundation.NewUUIDGenerator(nil).New()
	if err != nil {
		fmt.Fprintln(os.Stderr, "host controller identity is unavailable")
		return 1
	}
	authority, err := hostcontroller.NewSessionAuthority(string(instanceID), token, 12*time.Hour)
	if err != nil {
		fmt.Fprintln(os.Stderr, "host controller session authority is unavailable")
		return 1
	}
	static, err := webassets.NewDir(config.assetsDirectory)
	if err != nil {
		fmt.Fprintln(os.Stderr, "host controller web assets are unavailable")
		return 1
	}
	driver, err := hostcontroller.NewComposeDriver(hostcontroller.ComposeDriverOptions{
		Executable: "docker", Project: "deploy", BaseFile: config.composeFile,
		OverrideFile: config.grantOverride, EnvFile: config.environmentFile,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "host controller runtime driver is unavailable")
		return 1
	}
	repository, err := workspacepostgres.NewRepository(database.DB())
	if err != nil {
		fmt.Fprintln(os.Stderr, "host controller Workspace repository is unavailable")
		return 1
	}
	control, err := workspaceapplication.NewControlService(
		repository, repository, foundation.NewUUIDGenerator(nil), foundation.SystemClock{},
	)
	if err != nil {
		fmt.Fprintln(os.Stderr, "host controller Workspace control is unavailable")
		return 1
	}
	ownerID, err := foundation.NewUUIDGenerator(nil).New()
	if err != nil {
		fmt.Fprintln(os.Stderr, "host controller ownership identity is unavailable")
		return 1
	}
	store, err := hostcontroller.NewCoordinator(hostcontroller.CoordinatorOptions{
		Service: control, Runtime: driver, Validator: hostcontroller.PathValidator{}, OwnerID: ownerID,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "host controller switch coordinator is unavailable")
		return 1
	}
	defer store.Close()
	handler, err := hostcontroller.NewHandler(hostcontroller.HandlerOptions{
		Authority: authority, Store: store, Backend: hostcontroller.StateBackend{State: store, Runtime: driver}, Static: static,
		ExpectedHost: config.address, Origin: "http://" + config.address, SecureCookie: config.secureCookie,
		PathValidator: hostcontroller.PathValidator{},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "host controller HTTP boundary is unavailable")
		return 1
	}

	listener, err := net.Listen("tcp4", config.address)
	if err != nil {
		fmt.Fprintln(os.Stderr, "host controller loopback listener is unavailable")
		return 1
	}
	server := &http.Server{
		Addr: config.address, Handler: handler, ReadHeaderTimeout: controllerReadHeaderTimeout,
		ReadTimeout: controllerReadTimeout, IdleTimeout: controllerIdleTimeout,
		ErrorLog: log.New(os.Stderr, "host-controller: ", 0),
	}

	shutdownSignals := make(chan os.Signal, 1)
	signal.Notify(shutdownSignals, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(shutdownSignals)
	serveResult := make(chan error, 1)
	go func() { serveResult <- server.Serve(listener) }()
	reconcileContext, cancelReconcile := context.WithCancel(context.Background())
	reconcileDone := make(chan struct{})
	go func() {
		defer close(reconcileDone)
		runReconcileLoop(reconcileContext, store, controllerReconcileTimeout, controllerReconcileInterval, func(error) {
			fmt.Fprintln(os.Stderr, "host controller Workspace reconciliation will retry")
		})
	}()
	defer cancelReconcile()
	fmt.Fprintln(os.Stderr, "host controller listening on loopback")
	select {
	case serveErr := <-serveResult:
		cancelReconcile()
		if !errors.Is(serveErr, http.ErrServerClosed) {
			fmt.Fprintln(os.Stderr, "host controller server stopped unexpectedly")
			return 1
		}
	case <-shutdownSignals:
		cancelReconcile()
		shutdownContext, cancel := context.WithTimeout(context.Background(), controllerShutdownTimeout)
		defer cancel()
		if err := server.Shutdown(shutdownContext); err != nil {
			fmt.Fprintln(os.Stderr, "host controller shutdown failed")
			return 1
		}
		if serveErr := <-serveResult; !errors.Is(serveErr, http.ErrServerClosed) {
			fmt.Fprintln(os.Stderr, "host controller server stopped unexpectedly")
			return 1
		}
	}
	select {
	case <-reconcileDone:
	case <-time.After(controllerShutdownTimeout):
		fmt.Fprintln(os.Stderr, "host controller Workspace reconciliation shutdown timed out")
	}
	return 0
}

func runReconcileLoop(
	ctx context.Context,
	reconciler workspaceReconciler,
	attemptTimeout time.Duration,
	retryInterval time.Duration,
	report func(error),
) {
	if ctx == nil || reconciler == nil || attemptTimeout <= 0 || retryInterval <= 0 {
		return
	}
	for {
		attemptContext, cancelAttempt := context.WithTimeout(ctx, attemptTimeout)
		err := reconciler.Reconcile(attemptContext)
		cancelAttempt()
		if err == nil || ctx.Err() != nil {
			return
		}
		if report != nil {
			report(err)
		}
		timer := time.NewTimer(retryInterval)
		select {
		case <-timer.C:
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return
		}
	}
}

func parseConfig(arguments []string) (controllerConfig, error) {
	flags := flag.NewFlagSet("zhixu-host-controller", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	config := controllerConfig{}
	flags.StringVar(&config.address, "addr", "127.0.0.1:8080", "loopback listen address")
	flags.StringVar(&config.assetsDirectory, "assets", "", "built SPA directory")
	flags.StringVar(&config.stateDirectory, "state-dir", "", "protected controller state directory")
	flags.StringVar(&config.composeFile, "compose-file", "", "checked-in base Compose file")
	flags.StringVar(&config.environmentFile, "env-file", "", "protected Compose environment file")
	flags.StringVar(&config.grantOverride, "grant-override", "", "protected generated grant override")
	flags.IntVar(&config.bootstrapTokenFD, "bootstrap-token-fd", -1, "inherited bootstrap credential file descriptor")
	flags.IntVar(&config.databaseURLFD, "database-url-fd", -1, "inherited database credential file descriptor")
	flags.BoolVar(&config.secureCookie, "secure-cookie", false, "set Secure on the controller cookie")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 0 {
		return controllerConfig{}, errors.New("invalid flags")
	}
	host, port, err := net.SplitHostPort(config.address)
	if err != nil || host != "127.0.0.1" {
		return controllerConfig{}, errors.New("controller must listen on IPv4 loopback")
	}
	parsedPort, err := strconv.ParseUint(port, 10, 16)
	if err != nil || parsedPort == 0 {
		return controllerConfig{}, errors.New("controller port is invalid")
	}
	if config.assetsDirectory == "" || config.stateDirectory == "" || config.composeFile == "" ||
		config.environmentFile == "" || config.grantOverride == "" || config.bootstrapTokenFD < 3 ||
		config.databaseURLFD < 3 || config.databaseURLFD == config.bootstrapTokenFD {
		return controllerConfig{}, errors.New("required controller input is missing")
	}
	for _, path := range []string{config.assetsDirectory, config.stateDirectory, config.composeFile, config.environmentFile, config.grantOverride} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return controllerConfig{}, errors.New("controller paths must be canonical and absolute")
		}
	}
	if filepath.Dir(config.grantOverride) != config.stateDirectory {
		return controllerConfig{}, errors.New("grant override must be inside the state directory")
	}
	if config.assetsDirectory != filepath.Join(config.stateDirectory, "bundle", "web") {
		return controllerConfig{}, errors.New("web assets must use the protected bundle directory")
	}
	return config, nil
}

func readBootstrapToken(fileDescriptor int) (string, error) {
	file := os.NewFile(uintptr(fileDescriptor), "controller-bootstrap")
	if file == nil {
		return "", errors.New("bootstrap descriptor is invalid")
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, 130))
	if err != nil || len(raw) > 129 {
		return "", errors.New("bootstrap credential could not be read")
	}
	value := strings.TrimSuffix(string(raw), "\n")
	value = strings.TrimSuffix(value, "\r")
	if len(value) < 32 || len(value) > 128 || value != strings.TrimSpace(value) || strings.ContainsAny(value, "\r\n\x00") {
		return "", errors.New("bootstrap credential is invalid")
	}
	for _, character := range value {
		if !(character >= 'a' && character <= 'z') && !(character >= 'A' && character <= 'Z') &&
			!(character >= '0' && character <= '9') && character != '-' && character != '_' {
			return "", errors.New("bootstrap credential is invalid")
		}
	}
	return value, nil
}

func readDatabaseURL(fileDescriptor int) (string, error) {
	file := os.NewFile(uintptr(fileDescriptor), "controller-database")
	if file == nil {
		return "", errors.New("database descriptor is invalid")
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, 4098))
	if err != nil || len(raw) > 4097 {
		return "", errors.New("database credential could not be read")
	}
	value := strings.TrimSuffix(string(raw), "\n")
	value = strings.TrimSuffix(value, "\r")
	if len(value) == 0 || len(value) > 4096 || value != strings.TrimSpace(value) || strings.ContainsAny(value, "\r\n\x00") {
		return "", errors.New("database credential is invalid")
	}
	if !strings.HasPrefix(value, "postgres://") && !strings.HasPrefix(value, "postgresql://") {
		return "", errors.New("database credential scheme is invalid")
	}
	return value, nil
}

func validateInputFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("input is not a regular file")
	}
	return nil
}

func validateProtectedInputFile(path string) error {
	if err := validateInputFile(path); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		return errors.New("protected input permissions are invalid")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() {
		return errors.New("protected input owner is invalid")
	}
	return nil
}

func validateAssetsDirectory(path, stateDirectory string) error {
	if path != filepath.Join(stateDirectory, "bundle", "web") {
		return errors.New("web assets are outside the protected bundle")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("web assets are not a regular directory")
	}
	resolvedState, err := filepath.EvalSymlinks(stateDirectory)
	if err != nil {
		return errors.New("controller state path is unavailable")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != filepath.Join(resolvedState, "bundle", "web") {
		return errors.New("web assets contain an unsafe path alias")
	}
	return nil
}
