# Sunday-board

A personal tool that consolidates my own fantasy football rosters across
four leagues on three platforms into a single view, and produces a
start/sit and waiver checklist ahead of each weekly lineup deadline.

## Why

I play in four leagues with different scoring and roster rules: two on
Yahoo, one on ESPN, one on Sleeper. Each platform shows me one league at a
time. Neither shows me a player's status across every team I manage, which
is where most of my weekly decisions actually live.

## Status

In development. Single user, run locally.

## Scope

Read-only. The tool reads leagues, teams, rosters, players, player status,
and transactions for the leagues in which I am a rostered manager,
authenticated as my own account on each platform. It does not write
lineups, submit transactions, or read data from leagues I am not a member
of.

## Data sources

- Sleeper API (public, unauthenticated)
- ESPN Fantasy v3 (session cookies, own account)
- Yahoo Fantasy Sports API (OAuth, own account, read-only)

Fantasy data provided by Yahoo Fantasy.
