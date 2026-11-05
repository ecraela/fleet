// The demo supervises two unprivileged local processes. Production installations
// can use systemd or the upstream S6 profile with the same agent protocol.
package main

import (
	"log"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"
)

func start(name string, args ...string) (*exec.Cmd, <-chan error) {
	if name == "/usr/local/bin/dataplaneapi" {
		path := "/run/haproxy/dataplane.sock"
		if info, err := os.Lstat(path); err == nil {
			if info.Mode()&os.ModeSocket == 0 {
				log.Fatal("unexpected Data Plane API socket path")
			}
			if err = os.Remove(path); err != nil {
				log.Fatal("remove stale Data Plane API socket: ", err)
			}
		}
	}

	c := exec.Command(name, args...)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	if err := c.Start(); err != nil {
		log.Fatal("start demo process: ", err)
	}
	done := make(chan error, 1)
	go func() { done <- c.Wait() }()
	return c, done
}
func stop(c *exec.Cmd, done <-chan error) {
	_ = c.Process.Signal(syscall.SIGTERM)
	waitStopped(c, done)
}

func waitStopped(c *exec.Cmd, done <-chan error) {
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = c.Process.Kill()
		<-done
	}
}

func shutdown(proxy *exec.Cmd, proxyDone <-chan error, api *exec.Cmd, apiDone <-chan error) {
	// HAProxy soft-stop closes listeners and finishes established requests.
	_ = proxy.Process.Signal(syscall.SIGUSR1)
	_ = api.Process.Signal(syscall.SIGTERM)
	waitStopped(proxy, proxyDone)
	waitStopped(api, apiDone)
}

func main() {
	syscall.Umask(0007)
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGUSR1, os.Interrupt)
	proxy, proxyDone := start("/usr/local/sbin/haproxy", "-W", "-db", "-S", "/run/haproxy/master.sock,mode,660", "-f", "/var/lib/haproxy/haproxy.cfg")
	api, apiDone := start("/usr/local/bin/dataplaneapi", "-f", "/var/lib/haproxy/dataplaneapi.yaml")
	for {
		select {
		case <-signals:
			shutdown(proxy, proxyDone, api, apiDone)
			return
		case <-proxyDone:
			stop(api, apiDone)
			os.Exit(1)
		case <-apiDone:
			select {
			case <-signals:
				_ = proxy.Process.Signal(syscall.SIGUSR1)
				waitStopped(proxy, proxyDone)
				return
			case <-time.After(time.Second):
			}
			api, apiDone = start("/usr/local/bin/dataplaneapi", "-f", "/var/lib/haproxy/dataplaneapi.yaml")
		}
	}
}
