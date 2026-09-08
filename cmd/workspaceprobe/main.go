package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/rootgrant"
	workspacepostgres "github.com/CodeZen-Lizhi/zhixu/internal/workspace/adapter/postgres"
	workspaceapplication "github.com/CodeZen-Lizhi/zhixu/internal/workspace/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/workspace/candidateprobe"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

func main() { os.Exit(run(os.Args[1:])) }

func run(arguments []string) int {
	flags := flag.NewFlagSet("zhixu-workspace-probe", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	roleValue := flags.String("role", "", "candidate runtime role")
	initializeGit := flags.Bool("initialize-git", false, "initialize Git before verification")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "workspace candidate arguments are invalid")
		return candidateprobe.ExitConfiguration
	}
	role := workspacedomain.RuntimeRole(*roleValue)
	if !workspacedomain.ValidRuntimeRole(role) || (*initializeGit && role != workspacedomain.RuntimeRoleAPI) {
		fmt.Fprintln(os.Stderr, "workspace candidate role is invalid")
		return candidateprobe.ExitConfiguration
	}
	grant, err := rootgrant.LoadProcessGrantFromEnvironment()
	if err != nil {
		fmt.Fprintln(os.Stderr, "workspace candidate grant is invalid")
		return candidateprobe.ExitConfiguration
	}
	operationID, err := foundation.ParseID(os.Getenv(candidateprobe.EnvOperationID))
	if err != nil {
		fmt.Fprintln(os.Stderr, "workspace candidate operation is invalid")
		return candidateprobe.ExitConfiguration
	}
	fingerprint := os.Getenv(candidateprobe.EnvRootFingerprint)
	bindingVersion, err := strconv.ParseInt(os.Getenv(candidateprobe.EnvBindingVersion), 10, 64)
	if err != nil || bindingVersion < 1 || fingerprint == "" {
		fmt.Fprintln(os.Stderr, "workspace candidate binding is invalid")
		return candidateprobe.ExitConfiguration
	}
	probeContext, cancelProbe := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancelProbe()
	if err := candidateprobe.Verify(probeContext, grant, *initializeGit, candidateprobe.ExecGitRunner{}); err != nil {
		fmt.Fprintln(os.Stderr, "workspace candidate filesystem verification failed")
		return candidateprobe.ExitCode(err)
	}
	databaseURL, err := databaseURLFromEnvironment(os.LookupEnv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "workspace candidate database configuration is invalid")
		return candidateprobe.ExitConfiguration
	}
	database, err := platformpostgres.Open(probeContext, databaseURL, 2, 0)
	if err != nil {
		fmt.Fprintln(os.Stderr, "workspace candidate database is unavailable")
		return candidateprobe.ExitDatabaseUnavailable
	}
	defer database.Close()
	if err := database.Ping(probeContext); err != nil {
		fmt.Fprintln(os.Stderr, "workspace candidate database is unavailable")
		return candidateprobe.ExitDatabaseUnavailable
	}
	repository, err := workspacepostgres.NewGORMRepository(database)
	if err != nil {
		fmt.Fprintln(os.Stderr, "workspace candidate control repository is unavailable")
		return candidateprobe.ExitDatabaseUnavailable
	}
	service, err := workspaceapplication.NewControlService(
		repository, repository, foundation.NewUUIDGenerator(nil), foundation.SystemClock{},
	)
	if err != nil {
		fmt.Fprintln(os.Stderr, "workspace candidate control service is unavailable")
		return candidateprobe.ExitConfiguration
	}
	instanceID, err := foundation.NewUUIDGenerator(nil).New()
	if err != nil {
		fmt.Fprintln(os.Stderr, "workspace candidate identity is unavailable")
		return candidateprobe.ExitRuntimeRegistration
	}
	if _, err := service.RegisterRuntime(probeContext, workspaceapplication.RuntimeRegistration{
		Role: role, InstanceID: instanceID, WorkspaceID: grant.WorkspaceID(), OperationID: &operationID,
		GrantGeneration: grant.Generation(), RootFingerprint: fingerprint,
		BindingVersion: bindingVersion, Phase: workspacedomain.RuntimePhasePrepared,
	}); err != nil {
		fmt.Fprintln(os.Stderr, "workspace candidate registration failed")
		return candidateprobe.ExitRuntimeRegistration
	}
	return 0
}

func databaseURLFromEnvironment(lookup rootgrant.LookupEnv) (string, error) {
	if lookup == nil {
		return "", fmt.Errorf("database environment is unavailable")
	}
	read := func(key string) (string, error) {
		value, found := lookup(key)
		if !found || value == "" || value != strings.TrimSpace(value) || strings.ContainsAny(value, "\r\n\x00") {
			return "", fmt.Errorf("database setting %s is invalid", key)
		}
		return value, nil
	}
	host, err := read("ZHIXU_DATABASE_HOST")
	if err != nil {
		return "", err
	}
	port, found := lookup("ZHIXU_DATABASE_PORT")
	if !found || port == "" {
		port = "5432"
	}
	parsedPort, err := strconv.ParseUint(port, 10, 16)
	if err != nil || parsedPort == 0 || strconv.FormatUint(parsedPort, 10) != port {
		return "", fmt.Errorf("database port is invalid")
	}
	name, err := read("ZHIXU_DATABASE_NAME")
	if err != nil {
		return "", err
	}
	user, err := read("ZHIXU_DATABASE_USER")
	if err != nil {
		return "", err
	}
	password, err := read("ZHIXU_DATABASE_PASSWORD")
	if err != nil {
		return "", err
	}
	value := &url.URL{Scheme: "postgres", Host: net.JoinHostPort(host, port), Path: "/" + name}
	value.User = url.UserPassword(user, password)
	query := value.Query()
	query.Set("sslmode", "disable")
	value.RawQuery = query.Encode()
	return value.String(), nil
}
