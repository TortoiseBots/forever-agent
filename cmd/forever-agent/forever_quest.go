package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/world"
)

func foreverQuestShareCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	const usage = "usage: forever-agent forever quest-share [--rest host:port] [--rpc host:port] [--ca FILE] [--server-name NAME] [--build N] [--allow-instance-redirect] [--world-checks-build-auth-key] --account EMAIL --character NAME (--quest N | --accept | --decline) [--from NAME] [--wait DURATION] [--settle DURATION] [--stay DURATION] [--capture-dir DIR]"
	fs := flag.NewFlagSet("forever quest-share", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var common foreverCommon
	common.flags(fs)
	questSet := false
	var questID int
	fs.Func("quest", "quest id to share, or expected quest id with --accept/--decline", func(v string) error {
		questSet = true
		_, err := fmt.Sscan(v, &questID)
		return err
	})
	accept := fs.Bool("accept", false, "wait for a shared quest and accept it")
	decline := fs.Bool("decline", false, "wait for a shared quest and decline it")
	from := fs.String("from", "", "with --accept or --decline: accept/decline only a share from this character name")
	waitSet := false
	var waitDur time.Duration
	fs.Func("wait", "how long to wait for push results (--quest) or a shared quest (--accept/--decline), 0-2m", func(v string) error {
		waitSet = true
		d, err := time.ParseDuration(v)
		if err != nil {
			return err
		}
		waitDur = d
		return nil
	})
	settle := fs.Duration("settle", 4*time.Second, "how long to settle in the world before acting, 0-1m")
	stay := fs.Duration("stay", 2*time.Second, "how long to stay in the world after acting, 0-2m")
	captureDir := fs.String("capture-dir", "", "write the packets this run's handlers see into a new run directory under this path (keep it outside the repository)")

	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, usage)
		return exitUsage
	}
	if err := common.check("forever quest-share"); err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}
	if *accept && *decline {
		fmt.Fprintln(stderr, "forever quest-share: --accept and --decline are different answers to the same shared quest; choose one")
		return exitUsage
	}
	if !*accept && !*decline && !questSet {
		fmt.Fprintln(stderr, "forever quest-share: give --quest N to share, or --accept / --decline to answer a shared quest")
		fmt.Fprintln(stderr, usage)
		return exitUsage
	}
	if questSet && questID <= 0 {
		fmt.Fprintln(stderr, "forever quest-share: --quest must be a positive quest id")
		return exitUsage
	}
	if *from != "" && !*accept && !*decline {
		fmt.Fprintln(stderr, "forever quest-share: --from needs --accept or --decline")
		return exitUsage
	}
	if *settle < 0 || *settle > time.Minute {
		fmt.Fprintln(stderr, "forever quest-share: --settle must be between 0 and 1m")
		return exitUsage
	}
	if *stay < 0 || *stay > 2*time.Minute {
		fmt.Fprintln(stderr, "forever quest-share: --stay must be between 0 and 2m")
		return exitUsage
	}
	if !waitSet {
		if *accept || *decline {
			waitDur = 30 * time.Second
		} else {
			waitDur = 5 * time.Second
		}
	}
	if waitDur < 0 || waitDur > 2*time.Minute {
		fmt.Fprintln(stderr, "forever quest-share: --wait must be between 0 and 2m")
		return exitUsage
	}
	if err := common.passwordCheck("forever quest-share"); err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}

	mode := "share"
	if *accept {
		mode = "accept"
	} else if *decline {
		mode = "decline"
	}

	report, err := common.enter(ctx, "forever quest-share", stdout, world.Options{
		QuestShareCheck: true,
		QuestShareMode:  mode,
		QuestShareID:    int32(questID),
		QuestShareFrom:  *from,
		QuestShareWait:  waitDur,
		QuestShareStay:  *stay,
		Settle:          *settle,
		Deadline:        *settle + waitDur + *stay + 2*time.Minute,
		CaptureDir:      *captureDir,
	})
	if report != nil {
		printLinkLost(stdout, report)
		printQuestShareSummary(stdout, report)
		printPacketCounts(stdout, report)
	}
	if err != nil {
		if code, isLogin := loginExit(err, stderr); isLogin {
			return code
		}
		fmt.Fprintf(stderr, "forever quest-share: %v\n", err)
		if errors.Is(err, world.ErrLinkLost) {
			return exitDisconnected
		}
		return exitWorld
	}
	return exitOK
}

func printQuestShareSummary(w io.Writer, report *world.Report) {
	if report == nil || report.QuestShare == nil {
		return
	}
	q := report.QuestShare
	switch q.Mode {
	case "share":
		fmt.Fprintf(w, "quest-share: quest %d ok=%v, %d result(s)\n", q.QuestID, q.OK, len(q.Results))
		for _, r := range q.Results {
			who := safeName(r.Name)
			if who == "" {
				who = r.GUID.String()
			}
			fmt.Fprintf(w, "  %s (%s): %s (%d)\n", who, r.GUID.String(), r.Reason, r.Result)
		}
	case "accept":
		slotText := ""
		if q.Slot != nil {
			slotText = fmt.Sprintf(", slot %d", *q.Slot)
		}
		fmt.Fprintf(w, "quest-share: accept quest %d (%q) kind=%s from=%s ok=%v%s\n",
			q.QuestID, safeName(q.Title), q.ShareKind, safeName(q.From), q.OK, slotText)
	case "decline":
		fmt.Fprintf(w, "quest-share: decline quest %d (%q) kind=%s from=%s ok=%v\n",
			q.QuestID, safeName(q.Title), q.ShareKind, safeName(q.From), q.OK)
	}
	if q.Reason != "" {
		fmt.Fprintf(w, "quest-share: %s\n", q.Reason)
	}
}
