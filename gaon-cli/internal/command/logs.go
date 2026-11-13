package command

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/url"
	"strconv"
)

func runLogs(ctx context.Context, cl *cluster, args []string, out io.Writer) error {
	var follow bool
	var tailBytes int64
	var tailGiven bool
	operands, err := parseFlags("logs", args, func(fs *flag.FlagSet) {
		fs.BoolVar(&follow, "f", false, "follow the output")
		fs.BoolVar(&follow, "follow", false, "follow the output")
		fs.Func("tail-bytes", "start this many bytes from the end", func(v string) (err error) {
			tailGiven = true
			tailBytes, err = strconv.ParseInt(v, 10, 64)
			return err
		})
	})
	if err != nil {
		return err
	}
	if len(operands) != 1 || operands[0] == "" {
		return usagef("logs: want NAME")
	}
	query := url.Values{}
	if follow {
		query.Set("follow", "true")
	}
	if tailGiven {
		if tailBytes < 1 {
			return usagef("logs: --tail-bytes is %d, want an integer of at least 1", tailBytes)
		}
		query.Set("tailBytes", strconv.FormatInt(tailBytes, 10))
	}
	name := operands[0]
	resp, err := openSubresource(ctx, cl, "GET", name, "log", query, nil)
	if err != nil {
		return fmt.Errorf("logs microvm/%s: %w", name, err)
	}
	defer resp.Body.Close()
	// The body has no framing, so only a read that ends in io.EOF is the end of
	// the output; any other end is an aborted stream.
	if _, err := io.Copy(out, resp.Body); err != nil {
		if ctx.Err() != nil {
			return nil // the user ended a follow
		}
		return fmt.Errorf("logs microvm/%s: the stream was aborted, the output above may be incomplete: %w", name, err)
	}
	return nil
}
