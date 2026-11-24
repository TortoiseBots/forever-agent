---
type: Reference
title: Where the old bundle went
description: Every page of the former docs/okf bundle, its class, and the page in this manual that now holds it.
tags: [developers, docs, migration, history]
status: stable
generated: { by: omp/deepseek-v4.1-flash, at: 2026-11-24T21:55:00Z }
sources:
  - id: bundle
    resource: docs/manual/index.md
---

# What this is

The repository carried an older OKF bundle at `docs/okf`: 167 markdown files written for the Turtle-era agent. It was folded into this manual and removed, so there is one bundle. This page records every old page, how it was classified, and where its content lives now.

| Class | Pages | Meaning |
|---|---|---|
| COVERED | 75 | A page in this manual already covers it for Forever; the old page was dropped, and correct detail unique to it was moved in where it mattered. |
| FOREVER-VALID | 7 | It describes something that works on a ForeverCore server and was not covered; it was rewritten to this bundle's format and moved to the area named below. |
| TURTLE-ONLY | 72 | It describes the 1.12 Turtle stack that is still in the tree; it moved to `docs/manual/turtle/` with its content, and its frontmatter was brought to the bundle rules. |
| OBSOLETE | 13 | It describes the removed bundle itself (its index, log and writing rules) or the old README map; it was dropped. |

Every Turtle page starts with the line "This page describes the Turtle 1.12 stack. These commands refuse a ForeverCore server." and keeps the command refusals the code performs. The old area names are kept under `turtle/`, so links between Turtle pages still resolve.

# The old README's sections

The long README (1025 lines before it was replaced) was a front page and a reference in one. Its front-page matter
stayed in [README.md](../../../README.md); its reference matter moved to the pages below. "Kept" means the section
is still in README.md because it is repository-level text, not user documentation.

| Old README section | Where it is now |
|---|---|
| Title, status paragraph and the ticket-number references | [README.md](../../../README.md) and [what forever-agent is](../getting-started/what-is-forever-agent.md); the ticket numbers were replaced by plain statements |
| `## Turtle-only commands` build/status table | [What has been proven](../getting-started/what-is-forever-agent.md#what-has-been-proven-and-what-has-not) and [the Turtle section](../turtle/index.md) |
| The per-command reference under it (`forever login-check`, `world-check`, `walk-check`, `map-check`, `chat-check`, `party-check`, `cmd-check`, `addon-check`, `death-check`, `loot-check`, `object-use`, `fight-check`, `char-create`, `char-delete`, `route-walk`, the plan and minimap tools) | The area pages: [first login](../getting-started/first-login.md), [movement](../daemon/movement.md), [travel](../daemon/travel.md), [chat](../player-actions/chat.md), [groups](../player-actions/groups-raids-and-guild.md), [command channel](../getting-started/safety-and-gm-guards.md#the-command-permission-probe), [add-on protocol](../bots-and-raids/addon-protocol.md), [death and the corpse run](../daemon/death-and-corpse-run.md), [quests and loot](../player-actions/quests-and-loot.md), [spells and combat](../player-actions/spells-and-combat.md), [route tools](../dungeons/route-tools.md), [plan tools](../dungeons/plan-tools.md), [navmesh and maps](../dungeons/navmesh-and-maps.md) |
| `## Quick start` | [Install and build](../getting-started/install-and-build.md) and [your first login](../getting-started/first-login.md) |
| `## What you can do with it` | [What forever-agent is](../getting-started/what-is-forever-agent.md#which-commands-you-will-reach-for-first) and the area indexes |
| `## Scrub a Forever capture` | [Scrubbing a capture before sharing it](../testing/ci-and-local-checks.md#scrubbing-a-capture-before-sharing-it) and [Privacy and captures](privacy-and-captures.md) |
| `## Replay a Forever capture` | [Replaying a capture](../testing/traces-and-records.md#replaying-a-capture) |
| `## Documentation` | [The manual's index](../index.md) |
| `## Development` | [Contributing](contributing.md) |
| `## Licence` | Kept in [README.md](../../../README.md) and [LICENSE](../../../LICENSE): repository-level text, not a manual page |

# The table

## about

| Old page | Class | Where it is now |
|---|---|---|
| `docs/okf/about/conventions.md` | OBSOLETE | dropped |
| `docs/okf/about/index.md` | OBSOLETE | dropped |
| `docs/okf/about/layout.md` | COVERED | [developers/package-map.md](../developers/package-map.md) |
| `docs/okf/about/readme-map.md` | OBSOLETE | dropped |

## agent

| Old page | Class | Where it is now |
|---|---|---|
| `docs/okf/agent/brain-concept.md` | TURTLE-ONLY | [turtle/agent/brain-concept.md](../turtle/agent/brain-concept.md) |
| `docs/okf/agent/brain-guide.md` | TURTLE-ONLY | [turtle/agent/brain-guide.md](../turtle/agent/brain-guide.md) |
| `docs/okf/agent/death-guide.md` | FOREVER-VALID | [daemon/death-and-corpse-run.md](../daemon/death-and-corpse-run.md) |
| `docs/okf/agent/dungeon-guide.md` | TURTLE-ONLY | [turtle/agent/dungeon-guide.md](../turtle/agent/dungeon-guide.md) |
| `docs/okf/agent/first-rpc-tutorial.md` | COVERED | [daemon/rpc-basics.md](../daemon/rpc-basics.md) |
| `docs/okf/agent/index.md` | OBSOLETE | dropped |
| `docs/okf/agent/login-guide.md` | TURTLE-ONLY | [turtle/agent/login-guide.md](../turtle/agent/login-guide.md) |
| `docs/okf/agent/movement-guide.md` | COVERED | [daemon/movement.md](../daemon/movement.md) |
| `docs/okf/agent/overview.md` | COVERED | [getting-started/what-is-forever-agent.md](../getting-started/what-is-forever-agent.md) |
| `docs/okf/agent/rpc-advanced-reference.md` | TURTLE-ONLY | [turtle/agent/rpc-advanced-reference.md](../turtle/agent/rpc-advanced-reference.md) |
| `docs/okf/agent/rpc-dungeon-reference.md` | TURTLE-ONLY | [turtle/agent/rpc-dungeon-reference.md](../turtle/agent/rpc-dungeon-reference.md) |
| `docs/okf/agent/rpc-reference.md` | COVERED | [daemon/method-index.md](../daemon/method-index.md) |
| `docs/okf/agent/session-concept.md` | COVERED | [daemon/serve.md](../daemon/serve.md) |
| `docs/okf/agent/social-guide.md` | COVERED | [player-actions/index.md](../player-actions/index.md) |
| `docs/okf/agent/troubleshooting.md` | COVERED | [getting-started/troubleshooting.md](../getting-started/troubleshooting.md) |
| `docs/okf/agent/units-state.md` | COVERED | [daemon/status-and-world-reads.md](../daemon/status-and-world-reads.md) |
| `docs/okf/agent/world-concept.md` | COVERED | [daemon/status-and-world-reads.md](../daemon/status-and-world-reads.md) |

## combat

| Old page | Class | Where it is now |
|---|---|---|
| `docs/okf/combat/concept-brain-engine.md` | TURTLE-ONLY | [turtle/combat/concept-brain-engine.md](../turtle/combat/concept-brain-engine.md) |
| `docs/okf/combat/concept-eluna.md` | TURTLE-ONLY | [turtle/combat/concept-eluna.md](../turtle/combat/concept-eluna.md) |
| `docs/okf/combat/concept-spell-data.md` | COVERED | [player-actions/spells-and-combat.md](../player-actions/spells-and-combat.md) |
| `docs/okf/combat/concept-spell-oracle.md` | COVERED | [testing/spell-score.md](../testing/spell-score.md) |
| `docs/okf/combat/guide-addon-host.md` | TURTLE-ONLY | [turtle/combat/guide-addon-host.md](../turtle/combat/guide-addon-host.md) |
| `docs/okf/combat/guide-boss-tactics.md` | TURTLE-ONLY | [turtle/combat/guide-boss-tactics.md](../turtle/combat/guide-boss-tactics.md) |
| `docs/okf/combat/guide-dps-rogue-hunter-ret-warlock.md` | TURTLE-ONLY | [turtle/combat/guide-dps-rogue-hunter-ret-warlock.md](../turtle/combat/guide-dps-rogue-hunter-ret-warlock.md) |
| `docs/okf/combat/guide-druid-shaman.md` | TURTLE-ONLY | [turtle/combat/guide-druid-shaman.md](../turtle/combat/guide-druid-shaman.md) |
| `docs/okf/combat/guide-healer-priest.md` | TURTLE-ONLY | [turtle/combat/guide-healer-priest.md](../turtle/combat/guide-healer-priest.md) |
| `docs/okf/combat/guide-holy-paladin-rez.md` | TURTLE-ONLY | [turtle/combat/guide-holy-paladin-rez.md](../turtle/combat/guide-holy-paladin-rez.md) |
| `docs/okf/combat/guide-pets.md` | TURTLE-ONLY | [turtle/combat/guide-pets.md](../turtle/combat/guide-pets.md) |
| `docs/okf/combat/guide-queue-lft.md` | TURTLE-ONLY | [turtle/combat/guide-queue-lft.md](../turtle/combat/guide-queue-lft.md) |
| `docs/okf/combat/guide-run-brain.md` | TURTLE-ONLY | [turtle/combat/guide-run-brain.md](../turtle/combat/guide-run-brain.md) |
| `docs/okf/combat/guide-score-fight.md` | TURTLE-ONLY | [turtle/combat/guide-score-fight.md](../turtle/combat/guide-score-fight.md) |
| `docs/okf/combat/guide-write-rotation.md` | TURTLE-ONLY | [turtle/combat/guide-write-rotation.md](../turtle/combat/guide-write-rotation.md) |
| `docs/okf/combat/index.md` | OBSOLETE | dropped |
| `docs/okf/combat/overview.md` | TURTLE-ONLY | [turtle/combat/overview.md](../turtle/combat/overview.md) |
| `docs/okf/combat/reference-cli.md` | TURTLE-ONLY | [turtle/combat/reference-cli.md](../turtle/combat/reference-cli.md) |
| `docs/okf/combat/reference-rotation-format.md` | TURTLE-ONLY | [turtle/combat/reference-rotation-format.md](../turtle/combat/reference-rotation-format.md) |
| `docs/okf/combat/reference-rpc.md` | TURTLE-ONLY | [turtle/combat/reference-rpc.md](../turtle/combat/reference-rpc.md) |
| `docs/okf/combat/troubleshooting.md` | TURTLE-ONLY | [turtle/combat/troubleshooting.md](../turtle/combat/troubleshooting.md) |
| `docs/okf/combat/tutorial-first-rotation.md` | TURTLE-ONLY | [turtle/combat/tutorial-first-rotation.md](../turtle/combat/tutorial-first-rotation.md) |

## dungeons

| Old page | Class | Where it is now |
|---|---|---|
| `docs/okf/dungeons/first-run.md` | TURTLE-ONLY | [turtle/dungeons/first-run.md](../turtle/dungeons/first-run.md) |
| `docs/okf/dungeons/fleet.md` | COVERED | [bots-and-raids/fleet.md](../bots-and-raids/fleet.md) |
| `docs/okf/dungeons/hire-party.md` | TURTLE-ONLY | [turtle/dungeons/hire-party.md](../turtle/dungeons/hire-party.md) |
| `docs/okf/dungeons/index.md` | OBSOLETE | dropped |
| `docs/okf/dungeons/lft.md` | TURTLE-ONLY | [turtle/dungeons/lft.md](../turtle/dungeons/lft.md) |
| `docs/okf/dungeons/live-tuning.md` | TURTLE-ONLY | [turtle/dungeons/live-tuning.md](../turtle/dungeons/live-tuning.md) |
| `docs/okf/dungeons/loot-gossip.md` | TURTLE-ONLY | [turtle/dungeons/loot-gossip.md](../turtle/dungeons/loot-gossip.md) |
| `docs/okf/dungeons/modes.md` | TURTLE-ONLY | [turtle/dungeons/modes.md](../turtle/dungeons/modes.md) |
| `docs/okf/dungeons/overview.md` | TURTLE-ONLY | [turtle/dungeons/overview.md](../turtle/dungeons/overview.md) |
| `docs/okf/dungeons/play.md` | COVERED | [dungeons/dungeon-play.md](../dungeons/dungeon-play.md) |
| `docs/okf/dungeons/pulltest.md` | TURTLE-ONLY | [turtle/dungeons/pulltest.md](../turtle/dungeons/pulltest.md) |
| `docs/okf/dungeons/ref-cli.md` | TURTLE-ONLY | [turtle/dungeons/ref-cli.md](../turtle/dungeons/ref-cli.md) |
| `docs/okf/dungeons/ref-remote.md` | TURTLE-ONLY | [turtle/dungeons/ref-remote.md](../turtle/dungeons/ref-remote.md) |
| `docs/okf/dungeons/ref-run.md` | TURTLE-ONLY | [turtle/dungeons/ref-run.md](../turtle/dungeons/ref-run.md) |
| `docs/okf/dungeons/refusals-watchdog.md` | TURTLE-ONLY | [turtle/dungeons/refusals-watchdog.md](../turtle/dungeons/refusals-watchdog.md) |
| `docs/okf/dungeons/run-commander.md` | TURTLE-ONLY | [turtle/dungeons/run-commander.md](../turtle/dungeons/run-commander.md) |
| `docs/okf/dungeons/run-leader.md` | TURTLE-ONLY | [turtle/dungeons/run-leader.md](../turtle/dungeons/run-leader.md) |
| `docs/okf/dungeons/run-observe.md` | TURTLE-ONLY | [turtle/dungeons/run-observe.md](../turtle/dungeons/run-observe.md) |
| `docs/okf/dungeons/start-anywhere.md` | TURTLE-ONLY | [turtle/dungeons/start-anywhere.md](../turtle/dungeons/start-anywhere.md) |
| `docs/okf/dungeons/troubleshooting.md` | TURTLE-ONLY | [turtle/dungeons/troubleshooting.md](../turtle/dungeons/troubleshooting.md) |

## getting-started

| Old page | Class | Where it is now |
|---|---|---|
| `docs/okf/getting-started/command-map.md` | COVERED | [developers/adding-a-command.md](../developers/adding-a-command.md) |
| `docs/okf/getting-started/drive-rpc.md` | COVERED | [daemon/rpc-basics.md](../daemon/rpc-basics.md) |
| `docs/okf/getting-started/first-login.md` | TURTLE-ONLY | [turtle/getting-started/first-login.md](../turtle/getting-started/first-login.md) |
| `docs/okf/getting-started/fleet-party.md` | COVERED | [bots-and-raids/fleet.md](../bots-and-raids/fleet.md) |
| `docs/okf/getting-started/index.md` | OBSOLETE | dropped |
| `docs/okf/getting-started/install.md` | COVERED | [getting-started/install-and-build.md](../getting-started/install-and-build.md) |
| `docs/okf/getting-started/lease-slot-reference.md` | COVERED | [getting-started/settings-and-slots.md](../getting-started/settings-and-slots.md) |
| `docs/okf/getting-started/lease-slot.md` | COVERED | [getting-started/settings-and-slots.md](../getting-started/settings-and-slots.md) |
| `docs/okf/getting-started/log-in.md` | TURTLE-ONLY | [turtle/getting-started/log-in.md](../turtle/getting-started/log-in.md) |
| `docs/okf/getting-started/login-reference.md` | TURTLE-ONLY | [turtle/getting-started/login-reference.md](../turtle/getting-started/login-reference.md) |
| `docs/okf/getting-started/overview.md` | COVERED | [getting-started/what-is-forever-agent.md](../getting-started/what-is-forever-agent.md) |
| `docs/okf/getting-started/rpc-fleet-reference.md` | COVERED | [daemon/rpc-basics.md](../daemon/rpc-basics.md) |
| `docs/okf/getting-started/settings-concepts.md` | COVERED | [getting-started/settings-and-slots.md](../getting-started/settings-and-slots.md) |
| `docs/okf/getting-started/settings-reference.md` | TURTLE-ONLY | [turtle/getting-started/settings-reference.md](../turtle/getting-started/settings-reference.md) |
| `docs/okf/getting-started/slots-leases-concepts.md` | COVERED | [getting-started/settings-and-slots.md](../getting-started/settings-and-slots.md) |
| `docs/okf/getting-started/troubleshooting.md` | COVERED | [getting-started/troubleshooting.md](../getting-started/troubleshooting.md) |
| `docs/okf/getting-started/what-it-is.md` | COVERED | [getting-started/what-is-forever-agent.md](../getting-started/what-is-forever-agent.md) |
| `docs/okf/getting-started/write-settings.md` | TURTLE-ONLY | [turtle/getting-started/write-settings.md](../turtle/getting-started/write-settings.md) |

## navigation

| Old page | Class | Where it is now |
|---|---|---|
| `docs/okf/navigation/aggro.md` | FOREVER-VALID | [dungeons/aggro-estimate.md](../dungeons/aggro-estimate.md) |
| `docs/okf/navigation/check-commands.md` | COVERED | [dungeons/plan-tools.md](../dungeons/plan-tools.md) |
| `docs/okf/navigation/checks.md` | COVERED | [dungeons/plan-tools.md](../dungeons/plan-tools.md) |
| `docs/okf/navigation/client-data.md` | COVERED | [dungeons/navmesh-and-maps.md](../dungeons/navmesh-and-maps.md) |
| `docs/okf/navigation/commands.md` | COVERED | [dungeons/navmesh-and-maps.md](../dungeons/navmesh-and-maps.md) |
| `docs/okf/navigation/draw-map.md` | COVERED | [dungeons/navmesh-and-maps.md](../dungeons/navmesh-and-maps.md) |
| `docs/okf/navigation/first-map.md` | COVERED | [dungeons/navmesh-and-maps.md](../dungeons/navmesh-and-maps.md) |
| `docs/okf/navigation/index.md` | OBSOLETE | dropped |
| `docs/okf/navigation/map-snapshot.md` | COVERED | [daemon/status-and-world-reads.md](../daemon/status-and-world-reads.md) |
| `docs/okf/navigation/minimaps.md` | COVERED | [dungeons/navmesh-and-maps.md](../dungeons/navmesh-and-maps.md) |
| `docs/okf/navigation/navmesh.md` | COVERED | [dungeons/navmesh-and-maps.md](../dungeons/navmesh-and-maps.md) |
| `docs/okf/navigation/overview.md` | COVERED | [dungeons/navmesh-and-maps.md](../dungeons/navmesh-and-maps.md) |
| `docs/okf/navigation/path-check.md` | COVERED | [dungeons/navmesh-and-maps.md](../dungeons/navmesh-and-maps.md) |
| `docs/okf/navigation/patrols.md` | COVERED | [dungeons/route-tools.md](../dungeons/route-tools.md) |
| `docs/okf/navigation/rpc.md` | COVERED | [daemon/movement.md](../daemon/movement.md) |
| `docs/okf/navigation/serverpath.md` | COVERED | [dungeons/navmesh-and-maps.md](../dungeons/navmesh-and-maps.md) |
| `docs/okf/navigation/troubleshooting.md` | COVERED | [dungeons/plan-tools.md](../dungeons/plan-tools.md) |
| `docs/okf/navigation/walk-navmesh.md` | COVERED | [daemon/movement.md](../daemon/movement.md) |
| `docs/okf/navigation/world.md` | FOREVER-VALID | [dungeons/world-exports.md](../dungeons/world-exports.md) |

## observability

| Old page | Class | Where it is now |
|---|---|---|
| `docs/okf/observability/captures-concept.md` | COVERED | [developers/privacy-and-captures.md](../developers/privacy-and-captures.md) |
| `docs/okf/observability/captures-guide.md` | TURTLE-ONLY | [turtle/observability/captures-guide.md](../turtle/observability/captures-guide.md) |
| `docs/okf/observability/commands-reference.md` | COVERED | [testing/traces-and-records.md](../testing/traces-and-records.md) |
| `docs/okf/observability/dashboard-guide.md` | COVERED | [bots-and-raids/dashboard.md](../bots-and-raids/dashboard.md) |
| `docs/okf/observability/dashreplay-guide.md` | TURTLE-ONLY | [turtle/observability/dashreplay-guide.md](../turtle/observability/dashreplay-guide.md) |
| `docs/okf/observability/decisions-concept.md` | TURTLE-ONLY | [turtle/observability/decisions-concept.md](../turtle/observability/decisions-concept.md) |
| `docs/okf/observability/events-concept.md` | COVERED | [daemon/events-and-logs.md](../daemon/events-and-logs.md) |
| `docs/okf/observability/fight-score-guide.md` | COVERED | [testing/fight-score.md](../testing/fight-score.md) |
| `docs/okf/observability/first-dashboard.md` | COVERED | [bots-and-raids/dashboard.md](../bots-and-raids/dashboard.md) |
| `docs/okf/observability/index.md` | OBSOLETE | dropped |
| `docs/okf/observability/map-guide.md` | COVERED | [dungeons/navmesh-and-maps.md](../dungeons/navmesh-and-maps.md) |
| `docs/okf/observability/offline-checks-guide.md` | COVERED | [dungeons/plan-tools.md](../dungeons/plan-tools.md) |
| `docs/okf/observability/overview.md` | COVERED | [testing/overview.md](../testing/overview.md) |
| `docs/okf/observability/route-editing-guide.md` | FOREVER-VALID | [bots-and-raids/dashboard-route-editing.md](../bots-and-raids/dashboard-route-editing.md) |
| `docs/okf/observability/run-controls-guide.md` | COVERED | [bots-and-raids/dashboard.md](../bots-and-raids/dashboard.md) |
| `docs/okf/observability/scenarios-guide.md` | COVERED | [testing/scenarios.md](../testing/scenarios.md) |
| `docs/okf/observability/trace-checks-concept.md` | COVERED | [testing/traces-and-records.md](../testing/traces-and-records.md) |
| `docs/okf/observability/trace-score-guide.md` | COVERED | [testing/traces-and-records.md](../testing/traces-and-records.md) |
| `docs/okf/observability/trace-score-tutorial.md` | COVERED | [testing/traces-and-records.md](../testing/traces-and-records.md) |
| `docs/okf/observability/traces-concept.md` | COVERED | [testing/traces-and-records.md](../testing/traces-and-records.md) |
| `docs/okf/observability/troubleshooting.md` | COVERED | [testing/overview.md](../testing/overview.md) |

## plans

| Old page | Class | Where it is now |
|---|---|---|
| `docs/okf/plans/apply-route-edits.md` | COVERED | [dungeons/route-tools.md](../dungeons/route-tools.md) |
| `docs/okf/plans/check-a-plan.md` | COVERED | [dungeons/plan-tools.md](../dungeons/plan-tools.md) |
| `docs/okf/plans/dungeon-notes.md` | FOREVER-VALID | [dungeons/dungeon-notes.md](../dungeons/dungeon-notes.md) |
| `docs/okf/plans/export-a-plan.md` | COVERED | [dungeons/plan-tools.md](../dungeons/plan-tools.md) |
| `docs/okf/plans/first-plan-check.md` | COVERED | [dungeons/plan-tools.md](../dungeons/plan-tools.md) |
| `docs/okf/plans/index.md` | OBSOLETE | dropped |
| `docs/okf/plans/overview.md` | COVERED | [dungeons/plans.md](../dungeons/plans.md) |
| `docs/okf/plans/plan-format.md` | COVERED | [dungeons/plans.md](../dungeons/plans.md) |
| `docs/okf/plans/plan-lifecycle.md` | COVERED | [dungeons/plans.md](../dungeons/plans.md) |
| `docs/okf/plans/probe-a-plan.md` | COVERED | [dungeons/plan-tools.md](../dungeons/plan-tools.md) |
| `docs/okf/plans/record-a-patrol.md` | COVERED | [dungeons/route-tools.md](../dungeons/route-tools.md) |
| `docs/okf/plans/ref-check-tools.md` | COVERED | [dungeons/plan-tools.md](../dungeons/plan-tools.md) |
| `docs/okf/plans/ref-export-tools.md` | COVERED | [dungeons/plan-tools.md](../dungeons/plan-tools.md) |
| `docs/okf/plans/ref-route-oracle.md` | COVERED | [dungeons/route-tools.md](../dungeons/route-tools.md) |
| `docs/okf/plans/ref-scout-probe.md` | COVERED | [dungeons/plan-tools.md](../dungeons/plan-tools.md) |
| `docs/okf/plans/route-oracle.md` | COVERED | [dungeons/route-tools.md](../dungeons/route-tools.md) |
| `docs/okf/plans/run-leader-rows.md` | FOREVER-VALID | [dungeons/run-leader-rows.md](../dungeons/run-leader-rows.md) |
| `docs/okf/plans/scout-a-plan.md` | COVERED | [dungeons/plan-tools.md](../dungeons/plan-tools.md) |
| `docs/okf/plans/trash-check.md` | COVERED | [dungeons/plan-tools.md](../dungeons/plan-tools.md) |
| `docs/okf/plans/troubleshooting.md` | FOREVER-VALID | [dungeons/plan-troubleshooting.md](../dungeons/plan-troubleshooting.md) |
| `docs/okf/plans/write-a-plan.md` | COVERED | [dungeons/plans.md](../dungeons/plans.md) |

## raids

| Old page | Class | Where it is now |
|---|---|---|
| `docs/okf/raids/fault-injection.md` | TURTLE-ONLY | [turtle/raids/fault-injection.md](../turtle/raids/fault-injection.md) |
| `docs/okf/raids/first-raid-walk.md` | TURTLE-ONLY | [turtle/raids/first-raid-walk.md](../turtle/raids/first-raid-walk.md) |
| `docs/okf/raids/follow-player.md` | TURTLE-ONLY | [turtle/raids/follow-player.md](../turtle/raids/follow-player.md) |
| `docs/okf/raids/index.md` | OBSOLETE | dropped |
| `docs/okf/raids/instance-cap.md` | TURTLE-ONLY | [turtle/raids/instance-cap.md](../turtle/raids/instance-cap.md) |
| `docs/okf/raids/manage-instance-cap.md` | TURTLE-ONLY | [turtle/raids/manage-instance-cap.md](../turtle/raids/manage-instance-cap.md) |
| `docs/okf/raids/overview.md` | TURTLE-ONLY | [turtle/raids/overview.md](../turtle/raids/overview.md) |
| `docs/okf/raids/pair-reference.md` | TURTLE-ONLY | [turtle/raids/pair-reference.md](../turtle/raids/pair-reference.md) |
| `docs/okf/raids/place-mid-raid.md` | TURTLE-ONLY | [turtle/raids/place-mid-raid.md](../turtle/raids/place-mid-raid.md) |
| `docs/okf/raids/placement.md` | TURTLE-ONLY | [turtle/raids/placement.md](../turtle/raids/placement.md) |
| `docs/okf/raids/raid-autopilot-reference.md` | TURTLE-ONLY | [turtle/raids/raid-autopilot-reference.md](../turtle/raids/raid-autopilot-reference.md) |
| `docs/okf/raids/raid-suite-reference.md` | TURTLE-ONLY | [turtle/raids/raid-suite-reference.md](../turtle/raids/raid-suite-reference.md) |
| `docs/okf/raids/raid-walk-reference.md` | TURTLE-ONLY | [turtle/raids/raid-walk-reference.md](../turtle/raids/raid-walk-reference.md) |
| `docs/okf/raids/recover-wipe.md` | TURTLE-ONLY | [turtle/raids/recover-wipe.md](../turtle/raids/recover-wipe.md) |
| `docs/okf/raids/report-reference.md` | TURTLE-ONLY | [turtle/raids/report-reference.md](../turtle/raids/report-reference.md) |
| `docs/okf/raids/roster-format.md` | TURTLE-ONLY | [turtle/raids/roster-format.md](../turtle/raids/roster-format.md) |
| `docs/okf/raids/run-a-raid-walk.md` | TURTLE-ONLY | [turtle/raids/run-a-raid-walk.md](../turtle/raids/run-a-raid-walk.md) |
| `docs/okf/raids/run-autopilot.md` | TURTLE-ONLY | [turtle/raids/run-autopilot.md](../turtle/raids/run-autopilot.md) |
| `docs/okf/raids/run-raid-suite.md` | TURTLE-ONLY | [turtle/raids/run-raid-suite.md](../turtle/raids/run-raid-suite.md) |
| `docs/okf/raids/tiers.md` | TURTLE-ONLY | [turtle/raids/tiers.md](../turtle/raids/tiers.md) |
| `docs/okf/raids/troubleshooting.md` | TURTLE-ONLY | [turtle/raids/troubleshooting.md](../turtle/raids/troubleshooting.md) |
| `docs/okf/raids/until-and-goals.md` | TURTLE-ONLY | [turtle/raids/until-and-goals.md](../turtle/raids/until-and-goals.md) |
| `docs/okf/raids/walk-preflight.md` | TURTLE-ONLY | [turtle/raids/walk-preflight.md](../turtle/raids/walk-preflight.md) |

## Bundle root

| Old page | Class | Where it is now |
|---|---|---|
| `docs/okf/index.md` | OBSOLETE | dropped |
| `docs/okf/log.md` | OBSOLETE | dropped |

# Notes

* The old `index.md` of every area and the old root index and `log.md` are OBSOLETE: the manual has its own indexes and update log.
* `about/conventions.md` described the old bundle's own writing rules; `about/readme-map.md` mapped the long README that no longer exists. Neither has content for a reader of this manual.
* A COVERED page was dropped because the page named in the third column covers its subject for Forever. Correct detail unique to the old page was moved in where it mattered (the teardown lease flags are one example), and the seven FOREVER-VALID pages carry the larger gaps. The Turtle pages were kept because their stack is still in the tree and its commands still run against a Turtle server.
* The Turtle pages are grouped and indexed from [turtle/](../turtle/index.md).
