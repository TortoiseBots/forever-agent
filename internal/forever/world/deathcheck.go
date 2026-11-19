package world

// forever death-check: the command that lets the character die and brings it back. It does not kill the character
// (a console command or a mob does): it settles, waits for the death, then runs the release flow, or waits for a
// resurrection offer and accepts it, or uses the spirit healer. Every step is printed with its time; the fight's and
// the walk's "the character died" behaviour is unchanged.

import (
	"context"
	"errors"
	"time"
)

// ErrDeathNotSeen is death-check's timeout: nothing killed the character within --death-wait.
var ErrDeathNotSeen = errors.New("world: the character did not die")

// The command's defaults.
const (
	defaultDeathWait     = 5 * time.Minute
	defaultResurrectWait = 60 * time.Second
	defaultDeathSettle   = 3 * time.Second
)

// deathCheck is round 6's command. It returns the flow's own errors: a link loss, a release the server aborted, a
// corpse on another map (which the run names, because this client does not change maps by walking), a reclaim the
// server refused, a resurrection that did not happen.
func (s *session) deathCheck(ctx context.Context) error {
	// The summary is the report's view of the world, whatever the flow's outcome; it is how the command prints the
	// model's resurrection count. A character this flow revived and that woke up in combat is walked out of it, so
	// the logout the command ends with is not refused by the server. Only a revival of this run counts: a timeout,
	// a cancelled run or an error before a death never moves the character.
	defer func() {
		s.report.Objects = s.objectSummary()
		if s.deathState().revivedThisRun {
			s.leaveCombat(ctx)
		}
	}()
	settle := s.opts.Settle
	if settle <= 0 {
		settle = defaultDeathSettle
	}
	// The settle answers the login burst and applies the first values; a death during it is the point of the
	// command, so it is not an error here.
	if err := s.collect(ctx, settle); err != nil {
		return err
	}
	if s.mover == nil && !s.selfSeen {
		return ErrNotReady
	}
	now := s.transport().Now()
	s.observeSelf(now)
	d := s.deathState()
	if d.phase == DeathAlive {
		// A login that resurrects the character (the server's AT_LOGIN_RESURRECT) sent ResurrectPlayer's marker
		// during the burst. That is not this run's resurrection: the summary reports what this flow did, and a
		// character that never died here is never walked anywhere.
		d.revivedAt = time.Time{}
		d.revivedBy = ""
		d.revivedThisRun = false
	}
	s.opts.logf("death-check: the character is %s", d.phase)

	deathWait := s.opts.DeathWait
	if deathWait <= 0 {
		deathWait = defaultDeathWait
	}
	if d.phase == DeathAlive {
		s.opts.logf("death-check: waiting up to %s for the character to die (a console command or a mob does it)", deathWait)
		if err := s.waitDeath(ctx, "the death", deathWait, ErrDeathNotSeen, func() bool {
			return d.phase != DeathAlive
		}); err != nil {
			return err
		}
	}
	if d.phase == DeathAlive {
		return ErrDeathNotSeen
	}
	s.deathStep(s.transport().Now(), "the character is %s", d.phase)

	// --accept-resurrect: wait for an offer and answer it. A server that revives the character instead ends the
	// wait; no offer within the wait falls back to the release and the corpse run, so the command still brings the
	// character back.
	if s.opts.AcceptResurrect {
		resurrectWait := s.opts.ResurrectWait
		if resurrectWait <= 0 {
			resurrectWait = defaultResurrectWait
		}
		s.opts.logf("death-check: waiting up to %s for a resurrection offer", resurrectWait)
		err := s.waitDeath(ctx, "a resurrection offer", resurrectWait, ErrNoResurrectOffer, func() bool {
			offer := d.liveOffer(s.transport().Now())
			return (offer != nil && s.offerAllowed(offer)) || d.phase == DeathAlive
		})
		switch {
		case d.phase == DeathAlive:
			return nil
		case err == nil:
			if err := s.AcceptResurrect(ctx); err != nil {
				return err
			}
			if d.phase == DeathAlive {
				return nil
			}
			return ErrResurrectRefused
		case errors.Is(err, ErrNoResurrectOffer):
			s.opts.logf("death-check: no resurrection offer arrived; continuing with the release and the corpse run")
		default:
			return err
		}
	}

	if s.opts.UseSpiritHealer {
		if err := s.Release(ctx); err != nil {
			return err
		}
		if d.phase == DeathAlive {
			return nil
		}
		if err := s.SpiritHealer(ctx); err != nil {
			return err
		}
		if d.phase != DeathAlive {
			return ErrSpiritHealerRefused
		}
		return nil
	}

	if err := s.Release(ctx); err != nil {
		return err
	}
	if d.phase == DeathAlive {
		return nil
	}
	// The corpse location is a confirmation; the death position is a usable answer when the query is not. A corpse
	// on another map or on a transport is fatal: it is reported, not half-walked.
	if _, err := s.CorpseLocation(ctx); err != nil {
		if errors.Is(err, ErrCorpseOnAnotherMap) || errors.Is(err, ErrCorpseOnTransport) {
			return err
		}
		s.opts.logf("death-check: the corpse location answer is missing (%v); using the death position", err)
	}
	if err := s.RunToCorpse(ctx); err != nil {
		return err
	}
	if d.phase == DeathAlive {
		// The character came back during the run: there is nothing to reclaim.
		return nil
	}
	if err := s.ReclaimCorpse(ctx); err != nil {
		return err
	}
	if d.phase != DeathAlive {
		return ErrReclaimRefused
	}
	return nil
}
