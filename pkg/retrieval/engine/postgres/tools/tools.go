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
	"io/ioutil"
	"os"
	"os/exec"
	"path"
	"strings"
	"time"

	"github.com/AlekSi/pointer"
	"github.com/docker/cli/cli/streams"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/jsonmessage"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/pkg/errors"
	"github.com/shirou/gopsutil/host"

	"gitlab.com/postgres-ai/database-lab/pkg/log"
)

const (
	maxValuesToReturn     = 1
	essentialLogsInterval = "10s"

	// ViewLogsCmd tells the command to view docker container logs.
	ViewLogsCmd = "docker logs --since 1m -f"

	// PasswordLength defines length for autogenerated passwords.
	PasswordLength = 16
	// PasswordMinDigits defines minimum digits for autogenerated passwords.
	PasswordMinDigits = 4
	// PasswordMinSymbols defines minimum symbols for autogenerated passwords.
	PasswordMinSymbols = 0
)

// IsEmptyDirectory checks whether a directory is empty.
func IsEmptyDirectory(dir string) (bool, error) {
	f, err := os.Open(dir)
	if err != nil {
		return false, err
	}

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

// DetectPGVersion defines PostgreSQL version of PGDATA.
func DetectPGVersion(dataDir string) (string, error) {
	version, err := exec.Command("cat", fmt.Sprintf(`%s/PG_VERSION`, dataDir)).CombinedOutput()
	if err != nil {
		return "", err
	}

	return string(bytes.TrimSpace(version)), nil
}

// PGRunConfig provides configuration to start Postgres.
func PGRunConfig(pgDataDir, pgVersion string) types.ExecConfig {
	command := fmt.Sprintf("sudo -Eu postgres /usr/lib/postgresql/%s/bin/postgres -D %s >& /proc/1/fd/1", pgVersion, pgDataDir)

	return types.ExecConfig{
		AttachStdout: true,
		AttachStderr: true,
		Cmd:          []string{"bash", "-c", command},
		Env:          os.Environ(),
	}
}

// AddVolumesToHostConfig adds volumes to container host configuration depends on process environment.
func AddVolumesToHostConfig(ctx context.Context, dockerClient *client.Client, hostConfig *container.HostConfig,
	dataDir string) error {
	hostInfo, err := host.Info()
	if err != nil {
		return errors.Wrap(err, "failed to get host info")
	}

	log.Dbg("Virtualization system: ", hostInfo.VirtualizationSystem)

	if hostInfo.VirtualizationRole == "guest" {
		inspection, err := dockerClient.ContainerInspect(ctx, hostInfo.Hostname)
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

// RunPostgres runs Postgres inside.
func RunPostgres(ctx context.Context, dockerClient *client.Client, containerID, dataDir string) error {
	// Set permissions.
	if err := ExecCommand(ctx, dockerClient, containerID, types.ExecConfig{
		Cmd: []string{"chown", "-R", "postgres", dataDir},
	}); err != nil {
		return errors.Wrap(err, "failed to set permissions")
	}

	pgVersion, err := DetectPGVersion(dataDir)
	if err != nil {
		return errors.Wrap(err, "failed to detect PostgreSQL version")
	}

	startSyncCommand, err := dockerClient.ContainerExecCreate(ctx, containerID, PGRunConfig(dataDir, pgVersion))
	if err != nil {
		return errors.Wrap(err, "failed to create exec command")
	}

	attach, err := dockerClient.ContainerExecAttach(ctx, startSyncCommand.ID, types.ExecStartCheck{})
	if err != nil {
		return errors.Wrap(err, "failed to attach to exec command")
	}

	defer attach.Close()

	if err := InspectCommandResponse(ctx, dockerClient, containerID, startSyncCommand.ID); err != nil {
		return errors.Wrap(err, "failed to perform exec command")
	}

	return nil
}

// InspectCommandResponse inspects success of command execution.
func InspectCommandResponse(ctx context.Context, dockerClient *client.Client, containerID, commandID string) error {
	inspect, err := dockerClient.ContainerExecInspect(ctx, commandID)
	if err != nil {
		return errors.Wrap(err, "failed to create command")
	}

	if inspect.ExitCode == 0 {
		return nil
	}

	logs, err := dockerClient.ContainerLogs(ctx, containerID, types.ContainerLogsOptions{
		Since:      essentialLogsInterval,
		ShowStdout: true,
		ShowStderr: true,
	})
	if err != nil {
		return errors.Wrap(err, "failed to get container logs")
	}

	errorDetails, err := ioutil.ReadAll(logs)
	if err != nil {
		return errors.Wrap(err, "failed to get error logs")
	}

	defer func() { _ = logs.Close() }()

	return errors.Errorf("exit code: %d.\nContainer logs:\n%s", inspect.ExitCode, string(errorDetails))
}

// CheckContainerReadiness checks health and reports if container is ready.
func CheckContainerReadiness(ctx context.Context, dockerClient *client.Client, containerID string) error {
	for {
		select {
		case <-ctx.Done():
			return nil
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
				return errors.New("container health check failed")
			}

			healthCheckLength := len(resp.State.Health.Log)
			if healthCheckLength > 0 {
				lastHealthCheck := resp.State.Health.Log[healthCheckLength-1]
				if lastHealthCheck.ExitCode > 1 {
					return errors.Errorf("health check failed. Code: %v, Output: %v", lastHealthCheck.ExitCode, lastHealthCheck.Output)
				}
			}

			log.Msg(fmt.Sprintf("Container is not ready yet. The current state is %v.", resp.State.Health.Status))
		}

		time.Sleep(time.Second)
	}
}

// PrintContainerLogs prints container output.
func PrintContainerLogs(ctx context.Context, dockerClient *client.Client, containerName string) {
	containerLogs, err := dockerClient.ContainerLogs(ctx, containerName, types.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Since:      essentialLogsInterval,
		Details:    true,
	})

	if err != nil {
		log.Err("Failed to get container logs", err)
		return
	}

	if err := ProcessAttachResponse(ctx, containerLogs, os.Stdout); err != nil {
		log.Err("Failed to process attach response: ", err)
	}
}

// RemoveContainer stops and removes container.
func RemoveContainer(ctx context.Context, dockerClient *client.Client, containerID string, stopTimeout time.Duration) {
	log.Msg(fmt.Sprintf("Removing container ID: %v", containerID))

	if err := dockerClient.ContainerStop(ctx, containerID, pointer.ToDuration(stopTimeout)); err != nil {
		log.Err("Failed to stop container: ", err)
	}

	if err := dockerClient.ContainerRemove(ctx, containerID, types.ContainerRemoveOptions{
		Force: true,
	}); err != nil {
		log.Err("Failed to remove container: ", err)

		return
	}

	log.Msg(fmt.Sprintf("Container %q has been removed", containerID))
}

// PullImage pulls a Docker image.
func PullImage(ctx context.Context, dockerClient *client.Client, image string) error {
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
	execCfg.Tty = true

	execCommand, err := dockerClient.ContainerExecCreate(ctx, containerID, execCfg)
	if err != nil {
		return errors.Wrap(err, "failed to create command")
	}

	if err := dockerClient.ContainerExecStart(ctx, execCommand.ID, types.ExecStartCheck{}); err != nil {
		return errors.Wrap(err, "failed to start a command")
	}

	if err := InspectCommandResponse(ctx, dockerClient, containerID, execCommand.ID); err != nil {
		return errors.Wrap(err, "unsuccessful command response")
	}

	return nil
}

// ProcessAttachResponse reads and processes the cmd output.
func ProcessAttachResponse(ctx context.Context, reader io.Reader, output io.Writer) error {
	var errBuf bytes.Buffer

	outputDone := make(chan error)

	go func() {
		// StdCopy de-multiplexes the stream into two writers.
		_, err := stdcopy.StdCopy(output, &errBuf, reader)
		outputDone <- err
	}()

	select {
	case err := <-outputDone:
		if err != nil {
			return errors.Wrap(err, "failed to copy output")
		}

		break

	case <-ctx.Done():
		return ctx.Err()
	}

	if errBuf.Len() > 0 {
		return errors.New(errBuf.String())
	}

	return nil
}
