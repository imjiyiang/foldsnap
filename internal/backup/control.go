package backup

import (
	"context"
	"os/exec"
	"sync"
)

type controlKey struct{}

// Control owns a cooperative gate and the complete rsync process group. A pause
// is acknowledged only at a gate or after signalling a registered child group.
type Control struct {
	mu        sync.Mutex
	requested bool
	state     string
	resume    chan struct{}
	pid       int
	changed   func()
}

func NewControl(changed func()) *Control { return &Control{state: "running", changed: changed} }
func WithControl(ctx context.Context, c *Control) context.Context {
	return context.WithValue(ctx, controlKey{}, c)
}
func Controller(ctx context.Context) *Control { c, _ := ctx.Value(controlKey{}).(*Control); return c }
func (c *Control) State() string              { c.mu.Lock(); defer c.mu.Unlock(); return c.state }
func (c *Control) notify() {
	if c.changed != nil {
		c.changed()
	}
}
func (c *Control) Pause() error {
	c.mu.Lock()
	if c.requested {
		c.mu.Unlock()
		return nil
	}
	if c.pid != 0 {
		if err := pauseGroup(c.pid); err != nil {
			c.mu.Unlock()
			return err
		}
	}
	c.requested = true
	c.resume = make(chan struct{})
	c.state = "pausing"
	if c.pid != 0 {
		c.state = "paused"
	}
	c.mu.Unlock()
	c.notify()
	return nil
}
func (c *Control) Resume() error {
	c.mu.Lock()
	if !c.requested {
		c.mu.Unlock()
		return nil
	}
	if c.pid != 0 {
		if err := resumeGroup(c.pid); err != nil {
			c.mu.Unlock()
			return err
		}
	}
	c.requested = false
	c.state = "running"
	close(c.resume)
	c.mu.Unlock()
	c.notify()
	return nil
}
func Checkpoint(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c := Controller(ctx)
	if c == nil {
		return nil
	}
	for {
		c.mu.Lock()
		if !c.requested {
			c.mu.Unlock()
			return ctx.Err()
		}
		changed := c.state != "paused"
		c.state = "paused"
		wake := c.resume
		c.mu.Unlock()
		if changed {
			c.notify()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-wake:
		}
	}
}
func RunCommand(ctx context.Context, cmd *exec.Cmd) error {
	configureGroup(cmd)
	c := Controller(ctx)
	if c == nil {
		return cmd.Run()
	}
	for {
		if err := Checkpoint(ctx); err != nil {
			return err
		}
		c.mu.Lock()
		if c.requested {
			c.mu.Unlock()
			continue
		}
		err := cmd.Start()
		if err != nil {
			c.mu.Unlock()
			return err
		}
		c.pid = cmd.Process.Pid
		c.mu.Unlock()
		err = cmd.Wait()
		c.mu.Lock()
		c.pid = 0
		c.mu.Unlock()
		if err == nil {
			return Checkpoint(ctx)
		}
		return err
	}
}
