package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func (c *Client) buildLogs(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] != "logs" {
		return errors.New("use build logs --org ID --service ID --build ID [--follow]")
	}
	f := flags("build logs", c.Err)
	org := f.String("org", "", "Organization ID")
	service := f.String("service", "", "Service ID")
	build := f.String("build", "", "Build ID")
	follow := f.Bool("follow", false, "Follow until the build finishes")
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	if *org == "" || *service == "" || *build == "" || f.NArg() != 0 {
		return errors.New("--org, --service and --build required")
	}
	path := "/api/v1/orgs/" + url.PathEscape(*org) + "/services/" + url.PathEscape(*service) + "/workers/builds/" + url.PathEscape(*build) + "/logs"
	previous := ""
	for {
		pollSeconds := 0
		var result struct {
			State string `json:"state"`
			URL   string `json:"download_url"`
		}
		if err := c.request(ctx, "GET", path, nil, &result, false, func(headers http.Header) {
			pollSeconds, _ = strconv.Atoi(headers.Get("X-Log-Poll-Interval"))
		}); err != nil {
			return err
		}
		log := ""
		if result.URL != "" {
			var err error
			log, err = c.readBuildLogObject(ctx, result.URL)
			if err != nil {
				return err
			}
		}
		if strings.HasPrefix(log, previous) {
			_, _ = fmt.Fprint(c.Out, log[len(previous):])
		} else {
			_, _ = fmt.Fprint(c.Out, log)
		}
		previous = log
		if pollSeconds > 3600 {
			return errors.New("invalid log poll interval")
		}
		if !*follow || pollSeconds <= 0 {
			return nil
		}
		timer := time.NewTimer(time.Duration(pollSeconds) * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// Object requests deliberately carry no control-plane credentials.
func (c *Client) readBuildLogObject(ctx context.Context, target string) (string, error) {
	u, err := url.Parse(target)
	if err != nil || u.User != nil || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost"))) {
		return "", errors.New("invalid log object URL")
	}
	req, err := http.NewRequestWithContext(ctx, "GET", target, nil)
	if err != nil {
		return "", errors.New("invalid log object request")
	}
	client := &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("log redirects refused") }}
	resp, err := client.Do(req)
	if err != nil {
		return "", errors.New("unable to read build log object")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("log object returned HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32769))
	if err != nil {
		return "", errors.New("unable to read build log object")
	}
	if len(raw) > 32768 {
		return "", errors.New("build log exceeds size limit")
	}
	return string(raw), nil
}
