package objects

// The three acknowledgements a real client sends for the movement changes of a released spirit. They were cut from a
// local capture of a real session (fixture_ack_gen_test.go) and are rebuilt byte for byte from their parsed fields,
// which is what pins the writers' layout: the ack index the server's packet carried and, for a speed change, the
// announced speed (MovementAck::Read / MovementSpeedAck::Read, MovementPackets.cpp:910-926).

import (
	"fmt"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// parseSpeedAck reads one CMSG_MOVE_FORCE_*_SPEED_CHANGE_ACK: a MovementInfo, the ack index and the speed.
func parseSpeedAck(payload []byte, hook func(wire.GUID)) (MovementInfo, uint32, float32, error) {
	b := reader(payload, hook)
	mi, err := ReadMovementInfo(b)
	if err != nil {
		return mi, 0, 0, err
	}
	index, speed := b.ReadU32(), b.ReadFloat32()
	if b.Err() != nil || b.Remaining() != 0 {
		return mi, index, speed, fmt.Errorf("objects: a speed ack has %d byte(s) left: %v", b.Remaining(), b.Err())
	}
	return mi, index, speed, nil
}

// parseMovementAck reads one CMSG_MOVE_WATER_WALK_ACK: a MovementInfo and the ack index.
func parseMovementAck(payload []byte, hook func(wire.GUID)) (MovementInfo, uint32, error) {
	b := reader(payload, hook)
	mi, err := ReadMovementInfo(b)
	if err != nil {
		return mi, 0, err
	}
	index := b.ReadU32()
	if b.Err() != nil || b.Remaining() != 0 {
		return mi, index, fmt.Errorf("objects: a movement ack has %d byte(s) left: %v", b.Remaining(), b.Err())
	}
	return mi, index, nil
}

// TestFixtureMovementAckWriters rebuilds the captured acknowledgements byte for byte. The values are the capture's
// own (the ack index the server's speed packet carried, the announced speed), except the rewrites the fixture file
// records.
func TestFixtureMovementAckWriters(t *testing.T) {
	fx := loadFixtures(t)
	t.Run("force_run_speed_ack", func(t *testing.T) {
		want := fixturePayload(t, fx["force_run_speed_ack"])
		mi, index, speed, err := parseSpeedAck(want, nil)
		if err != nil {
			t.Fatal(err)
		}
		if index == 0 || speed <= 0 {
			t.Fatalf("the captured ack carries index %d and speed %v", index, speed)
		}
		if got := BuildSpeedAck(mi, index, speed); string(got) != string(want) {
			t.Fatalf("the writer's bytes differ\n got % X\nwant % X", got, want)
		}
	})
	t.Run("force_swim_speed_ack", func(t *testing.T) {
		want := fixturePayload(t, fx["force_swim_speed_ack"])
		mi, index, speed, err := parseSpeedAck(want, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := BuildSpeedAck(mi, index, speed); string(got) != string(want) {
			t.Fatalf("the writer's bytes differ\n got % X\nwant % X", got, want)
		}
	})
	t.Run("water_walk_ack", func(t *testing.T) {
		want := fixturePayload(t, fx["water_walk_ack"])
		mi, index, err := parseMovementAck(want, nil)
		if err != nil {
			t.Fatal(err)
		}
		if mi.Flags&FlagWaterwalking == 0 {
			t.Fatal("the captured water-walk ack does not carry the water-walking flag")
		}
		if got := BuildMovementAck(mi, index); string(got) != string(want) {
			t.Fatalf("the writer's bytes differ\n got % X\nwant % X", got, want)
		}
	})
}
