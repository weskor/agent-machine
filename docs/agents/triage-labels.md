# Triage Labels

The Matt Pocock skills speak in terms of five canonical triage roles. In this repo those roles map to Linear workflow states rather than GitHub labels.

| Label in mattpocock/skills | Linear state for this repo | Meaning |
| --- | --- | --- |
| `needs-triage` | `Todo` | Maintainer needs to evaluate or shape the issue. |
| `needs-info` | `Needs Info` | Waiting on the reporter/maintainer for more detail. |
| `ready-for-agent` | `Ready for Agent` | Fully specified and safe for Pi Symphony to claim. |
| `ready-for-human` | `Human Review` | Requires human review, repair, merge, or implementation. |
| `wontfix` | `Canceled` | Will not be actioned. |

Use the Linear state names above when moving issues. Do not invent GitHub labels unless a future issue explicitly changes the tracker policy.
