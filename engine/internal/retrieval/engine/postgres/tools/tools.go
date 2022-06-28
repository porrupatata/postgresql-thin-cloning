/*
2020 © Postgres.ai
*/

// TODO(akartasov): Refactor tools package: divide to specific subpackages.

// Package tools provides helpers to initialize data.
package tools

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/AlekSi/pointer"
	"github.com/docker/cli/cli/streams"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/docker/errdefs"
	"github.com/docker/docker/pkg/jsonmessage"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/pkg/errors"
	"github.com/sethvargo/go-password/password"
	"github.com/shirou/gopsutil/host"

	"gitlab.com/postgres-ai/database-lab/v3/internal/retrieval/engine/postgres/tools/defaults"
	"gitlab.com/postgres-ai/database-lab/v3/pkg/log"
)

const (
	maxValuesToReturn     = 1
	essentialLogsInterval = "10s"

	// DefaultStopTimeout defines the default timeout for Postgres stop.
	DefaultStopTimeout = 600

	// ViewLogsCmd tells the command to view docker container logs.
	ViewLogsCmd = "docker logs --since 1m -f"

	// passwordLength defines length for autogenerated passwords.
	passwordLength = 16
	// passwordMinDigits defines minimum digits for autogenerated passwords.
	passwordMinDigits = 4
	// passwordMinSymbols defines minimum symbols for autogenerated passwords.
	passwordMinSymbols = 0
)

// ErrHealthCheck defines a health check errors.
type ErrHealthCheck struct {
	ExitCode int
	Output   string
}

// Error prints a health check error.
func (e *ErrHealthCheck) Error() string {
	return fmt.Sprintf("health check failed. Code: %d, Output: %s", e.ExitCode, e.Output)
}

// GeneratePassword generates a new password.
func GeneratePassword() (string, error) {
	return password.Generate(passwordLength, passwordMinDigits, passwordMinSymbols, false, true)
}

// IsEmptyDirectory checks whether a directory is empty.
func IsEmptyDirectory(dir string) (bool, error) {
	f, err := os.Open(dir)
	if err != nil {
		return false, err
	}

	defer func() { _ = f.Close() }()

	names, err := f.Readdirnames(maxValuesToReturn)
	if err != nil && err != io.EOF {
		return false, err
	}

	return len(names) == 0, nil
}

// TouchFile creates an empty file.
func TouchFile(filename string) error {
	file, err := os.Create(filename)
	if err != nil {
		return errors.Wrapf(err, "failed to touch file: %s", filename)
	}

	defer func() { _ = file.Close() }()

	return nil
}

// TrimPort trims a port from a hostname if the port exists.
func TrimPort(hostname string) string {
	if idx := strings.Index(hostname, ":"); idx != -1 {
		return hostname[:idx]
	}

	return hostname
}

// DetectPGVersion defines PostgreSQL version of PGDATA.
func DetectPGVersion(dataDir string) (float64, error) {
	version, err := exec.Command("cat", fmt.Sprintf(`%s/PG_VERSION`, dataDir)).CombinedOutput()
	if err != nil {
		return 0, err
	}

	pgVersion, err := strconv.ParseFloat(string(bytes.TrimSpace(version)), 64)
	if err != nil {
		return 0, errors.Wrap(err, "failed to parse PostgreSQL version")
	}

	return pgVersion, nil
}

// AddVolumesToHostConfig adds volumes to container host configuration depends on process environment.
func AddVolumesToHostConfig(ctx context.Context, docker *client.Client, hostConfig *container.HostConfig, dataDir string) error {
	hostInfo, err := host.Info()
	if err != nil {
		return errors.Wrap(err, "failed to get host info")
	}

	log.Dbg("Virtualization system: ", hostInfo.VirtualizationSystem)

	if hostInfo.VirtualizationRole == "guest" {
		inspection, err := docker.ContainerInspect(ctx, hostInfo.Hostname)
		if err != nil {
			return err
		}

		hostConfig.Mounts = GetMountsFromMountPoints(dataDir, inspection.Mounts)

		log.Dbg(hostConfig.Mounts)
	} else {
		hostConfig.Mounts = append(hostConfig.Mounts, mount.Mount{
			Type:   mount.TypeBind,
			Source: dataDir,
			Target: dataDir,
		})
	}

	return nil
}

// GetMountsFromMountPoints creates a list of mounts.
func GetMountsFromMountPoints(dataDir string, mountPoints []types.MountPoint) []mount.Mount {
	mounts := make([]mount.Mount, 0, len(mountPoints))

	for _, mountPoint := range mountPoints {
		// Rewrite mounting to data directory.
		if strings.HasPrefix(dataDir, mountPoint.Destination) {
			suffix := strings.TrimPrefix(dataDir, mountPoint.Destination)
			mountPoint.Source = path.Join(mountPoint.Source, suffix)
			mountPoint.Destination = dataDir
		}

		mounts = append(mounts, mount.Mount{
			Type:     mountPoint.Type,
			Source:   mountPoint.Source,
			Target:   mountPoint.Destination,
			ReadOnly: !mountPoint.RW,
			BindOptions: &mount.BindOptions{
				Propagation: mountPoint.Propagation,
			},
		})
	}

	return mounts
}

// InitDB stops Postgres inside container.
func InitDB(ctx context.Context, dockerClient *client.Client, containerID string) error {
	initCommand := []string{"sh", "-c", `su postgres -c "/usr/lib/postgresql/${PG_MAJOR}/bin/pg_ctl initdb -D ${PGDATA}"`}

	log.Dbg("Init db", initCommand)

	out, err := ExecCommandWithOutput(ctx, dockerClient, containerID, types.ExecConfig{
		Tty: true,
		Cmd: initCommand,
	})

	if err != nil {
		return errors.Wrap(err, "failed to init Postgres")
	}

	log.Dbg(out)

	return nil
}

// MakeDir creates a new directory inside a container.
func MakeDir(ctx context.Context, dockerClient *client.Client, dumpContID, dataDir string) error {
	mkdirCmd := []string{"mkdir", "-p", dataDir}

	log.Msg("Running mkdir command: ", mkdirCmd)

	if out, err := ExecCommandWithOutput(ctx, dockerClient, dumpContID, types.ExecConfig{
		Cmd:  mkdirCmd,
		User: defaults.Username,
	}); err != nil {
		log.Dbg(out)
		return errors.Wrap(err, "failed to create a temp location")
	}

	return nil
}

// LsContainerDirectory lists content of the directory in a container.
func LsContainerDirectory(ctx context.Context, dockerClient *client.Client, containerID, dir string) ([]string, error) {
	lsCommand := []string{"ls", "-A", dir, "--color=never"}

	log.Dbg("Check directory: ", lsCommand)

	out, err := ExecCommandWithOutput(ctx, dockerClient, containerID, types.ExecConfig{
		Tty: true,
		Cmd: lsCommand,
	})

	if err != nil {
		log.Dbg(out)
		return nil, errors.Wrap(err, "failed to init Postgres")
	}

	return strings.Fields(out), nil
}

// StartPostgres stops Postgres inside container.
func StartPostgres(ctx context.Context, dockerClient *client.Client, containerID string, timeout int) error {
	log.Dbg("Start Postgres")

	startCommand := []string{"sh", "-c",
		fmt.Sprintf(`su postgres -c "/usr/lib/postgresql/${PG_MAJOR}/bin/pg_ctl -D ${PGDATA} -w --timeout %d start"`, timeout)}

	log.Msg("Starting PostgreSQL instance", startCommand)

	out, err := ExecCommandWithOutput(ctx, dockerClient, containerID, types.ExecConfig{
		Tty: true,
		Cmd: startCommand,
	})

	if err != nil {
		return errors.Wrap(err, "failed to stop Postgres")
	}

	log.Dbg(out)

	return nil
}

// RunCheckpoint runs checkpoint, usually before the postgres stop
func RunCheckpoint(
	ctx context.Context,
	dockerClient *client.Client,
	containerID string,
	user string,
	database string,
) error {
	commandCheckpoint := []string{
		"psql",
		"-U",
		user,
		"-d",
		database,
		"-XAtc",
		"checkpoint",
	}
	log.Msg("Run checkpoint command", commandCheckpoint)

	output, err := ExecCommandWithOutput(
		ctx,
		dockerClient,
		containerID,
		types.ExecConfig{Cmd: commandCheckpoint},
	)
	if err != nil {
		return errors.Wrap(err, "failed to make checkpoint")
	}

	log.Msg("Checkpoint result: ", output)

	return nil
}

// StopPostgres stops Postgres inside container.
func StopPostgres(ctx context.Context, dockerClient *client.Client, containerID, dataDir string, timeout int) error {
	pgVersion, err := DetectPGVersion(dataDir)
	if err != nil {
		return errors.Wrap(err, "failed to detect PostgreSQL version")
	}

	stopCommand := []string{fmt.Sprintf("/usr/lib/postgresql/%g/bin/pg_ctl", pgVersion),
		"-D", dataDir, "-w", "--timeout", strconv.Itoa(timeout), "stop"}

	log.Msg("Stopping PostgreSQL instance", stopCommand)

	if output, err := ExecCommandWithOutput(ctx, dockerClient, containerID, types.ExecConfig{
		User: defaults.Username,
		Cmd:  stopCommand,
	}); err != nil {
		log.Dbg(output)
		return errors.Wrap(err, "failed to stop Postgres")
	}

	return nil
}

// CheckContainerReadiness checks health and reports if container is ready.
func CheckContainerReadiness(ctx context.Context, dockerClient *client.Client, containerID string) (err error) {
	log.Msg("Check container readiness: ", containerID)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		resp, err := dockerClient.ContainerInspect(ctx, containerID)
		if err != nil {
			return errors.Wrapf(err, "failed to inspect container %s", containerID)
		}

		if resp.State != nil && resp.State.Health != nil {
			switch resp.State.Health.Status {
			case types.Healthy:
				return nil

			case types.Unhealthy:
				return fmt.Errorf("container health check failed. The maximum number of attempts has reached: %d",
					resp.Config.Healthcheck.Retries)
			}

			if healthCheckLength := len(resp.State.Health.Log); healthCheckLength > 0 {
				if lastHealthCheck := resp.State.Health.Log[healthCheckLength-1]; lastHealthCheck.ExitCode > 1 {
					return &ErrHealthCheck{
						ExitCode: lastHealthCheck.ExitCode,
						Output:   lastHealthCheck.Output,
					}
				}
			}
		}

		time.Sleep(time.Second)
	}
}

// PrintContainerLogs prints container output.
func PrintContainerLogs(ctx context.Context, dockerClient *client.Client, containerID string) {
	logs, err := dockerClient.ContainerLogs(ctx, containerID, types.ContainerLogsOptions{
		Since:      essentialLogsInterval,
		ShowStdout: true,
		ShowStderr: true,
	})
	if err != nil {
		log.Err(errors.Wrapf(err, "failed to get logs from container %s", containerID))
		return
	}

	defer func() { _ = logs.Close() }()

	wb := new(bytes.Buffer)

	if _, err := io.Copy(wb, logs); err != nil {
		log.Err(errors.Wrapf(err, "failed to read logs from container %s", containerID))
		return
	}

	log.Msg("Container logs:\n", wb.String())
}

// PrintLastPostgresLogs prints Postgres container logs.
func PrintLastPostgresLogs(ctx context.Context, dockerClient *client.Client, containerID, clonePath string) {
	command := []string{"bash", "-c", "tail -n 20 $(ls -t " + clonePath + "/log/*.csv | tail -n 1)"}

	output, err := ExecCommandWithOutput(ctx, dockerClient, containerID, types.ExecConfig{Cmd: command})
	if err != nil {
		log.Err(errors.Wrap(err, "failed to read Postgres logs"))
	}

	log.Msg("Postgres logs: ", output)
}

// StopContainer stops container.
func StopContainer(ctx context.Context, dockerClient *client.Client, containerID string, stopTimeout time.Duration) {
	log.Msg(fmt.Sprintf("Stopping container ID: %v", containerID))

	if err := dockerClient.ContainerStop(ctx, containerID, pointer.ToDuration(stopTimeout)); err != nil {
		log.Err("Failed to stop container: ", err)
	}

	log.Msg(fmt.Sprintf("Container %q has been stopped", containerID))
}

// RemoveContainer stops and removes container.
func RemoveContainer(ctx context.Context, dockerClient *client.Client, containerID string, stopTimeout time.Duration) {
	log.Msg(fmt.Sprintf("Removing container ID: %v", containerID))

	if err := dockerClient.ContainerStop(ctx, containerID, pointer.ToDuration(stopTimeout)); err != nil {
		log.Err("Failed to stop container: ", err)
	}

	log.Msg(fmt.Sprintf("Container %q has been stopped", containerID))

	if err := dockerClient.ContainerRemove(ctx, containerID, types.ContainerRemoveOptions{
		RemoveVolumes: true,
		Force:         true,
	}); err != nil {
		log.Err("Failed to remove container: ", err)

		return
	}

	log.Msg(fmt.Sprintf("Container %q has been removed", containerID))
}

// PullImage pulls a Docker image.
func PullImage(ctx context.Context, dockerClient *client.Client, image string) error {
	inspectionResult, _, err := dockerClient.ImageInspectWithRaw(ctx, image)
	if err != nil {
		if _, ok := err.(errdefs.ErrNotFound); !ok {
			return errors.Wrapf(err, "failed to inspect image %q", image)
		}
	}

	if err == nil && inspectionResult.ID != "" {
		log.Msg(fmt.Sprintf("Docker image %q already exists locally", image))
		return nil
	}

	pullOutput, err := dockerClient.ImagePull(ctx, image, types.ImagePullOptions{})
	if err != nil {
		return errors.Wrapf(err, "failed to pull image %q", image)
	}

	defer func() { _ = pullOutput.Close() }()

	if err := jsonmessage.DisplayJSONMessagesToStream(pullOutput, streams.NewOut(os.Stdout), nil); err != nil {
		log.Err("Failed to render pull image output: ", err)
	}

	return nil
}

// ExecCommand runs command in Docker container.
func ExecCommand(ctx context.Context, dockerClient *client.Client, containerID string, execCfg types.ExecConfig) error {
	execCfg.AttachStdout = true
	execCfg.AttachStderr = true

	execCommand, err := dockerClient.ContainerExecCreate(ctx, containerID, execCfg)
	if err != nil {
		return errors.Wrap(err, "failed to create command")
	}

	if err := dockerClient.ContainerExecStart(ctx, execCommand.ID, types.ExecStartCheck{}); err != nil {
		return errors.Wrap(err, "failed to start a command")
	}

	if err := inspectCommandExitCode(ctx, dockerClient, execCommand.ID); err != nil {
		return errors.Wrap(err, "unsuccessful command response")
	}

	return nil
}

// inspectCommandExitCode inspects success of command execution.
func inspectCommandExitCode(ctx context.Context, dockerClient *client.Client, commandID string) error {
	inspect, err := dockerClient.ContainerExecInspect(ctx, commandID)
	if err != nil {
		return errors.Wrap(err, "failed to create command")
	}

	if inspect.Running {
		log.Dbg("command is still running")
	}

	if inspect.ExitCode == 0 {
		return nil
	}

	return errors.Errorf("exit code: %d", inspect.ExitCode)
}

// ExecCommandWithOutput runs command in Docker container and returns the command output.
func ExecCommandWithOutput(ctx context.Context, dockerClient *client.Client, containerID string, execCfg types.ExecConfig) (string, error) {
	execCfg.AttachStdout = true
	execCfg.AttachStderr = true

	execCommand, err := dockerClient.ContainerExecCreate(ctx, containerID, execCfg)

	if err != nil {
		return "", errors.Wrap(err, "failed to create an exec command")
	}

	attachResponse, err := dockerClient.ContainerExecAttach(ctx, execCommand.ID, types.ExecStartCheck{})
	if err != nil {
		return "", errors.Wrap(err, "failed to attach to exec command")
	}

	defer attachResponse.Close()

	output, err := processAttachResponse(ctx, attachResponse.Reader)
	if err != nil {
		return string(output), errors.Wrap(err, "failed to read response of exec command")
	}

	inspection, err := dockerClient.ContainerExecInspect(ctx, execCommand.ID)
	if err != nil {
		return "", fmt.Errorf("failed to inspect an exec process: %w", err)
	}

	if inspection.ExitCode != 0 {
		err = fmt.Errorf("exit code: %d", inspection.ExitCode)
	}

	return string(output), err
}

// processAttachResponse reads and processes the cmd output.
func processAttachResponse(ctx context.Context, reader io.Reader) ([]byte, error) {
	var outBuf, errBuf bytes.Buffer

	outputDone := make(chan error)

	go func() {
		// StdCopy de-multiplexes the stream into two writers.
		_, err := stdcopy.StdCopy(&outBuf, &errBuf, reader)
		outputDone <- err
	}()

	select {
	case err := <-outputDone:
		if err != nil {
			return nil, errors.Wrap(err, "failed to copy output")
		}

		break

	case <-ctx.Done():
		return nil, ctx.Err()
	}

	if errBuf.Len() > 0 {
		return nil, errors.New(errBuf.String())
	}

	return bytes.TrimSpace(outBuf.Bytes()), nil
}

// CreateContainerIfMissing create a new container if there is no other container with the same name, if the container
// exits returns existing container id.
func CreateContainerIfMissing(ctx context.Context, docker *client.Client, containerName string,
	config *container.Config, hostConfig *container.HostConfig) (string, error) {
	containerData, err := docker.ContainerInspect(ctx, containerName)

	if err == nil {
		return containerData.ID, nil
	}

	createdContainer, err := docker.ContainerCreate(ctx, config, hostConfig, &network.NetworkingConfig{},
		nil, containerName,
	)
	if err != nil {
		return "", err
	}

	return createdContainer.ID, nil
}
