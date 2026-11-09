# Signed release compatibility entries

Before tagging a Fleet release, add `<exact-tag>.json` in this directory. The
release workflow refuses to publish without it. For example, after qualifying
a candidate `v0.1.0-rc.22` with an `rc.21` manager and agent:

```json
{
  "version": "v0.1.0-rc.22",
  "manager": {"min": "v0.1.0-rc.21", "max": "v0.1.0-rc.22"},
  "agent": {"min": "v0.1.0-rc.21", "max": "v0.1.0-rc.22"}
}
```

`manager` describes the manager versions a candidate **agent** supports;
`agent` describes the agent versions a candidate **manager** supports. Bounds
are inclusive exact-release versions. The candidate's own version must be
inside both ranges. Do not copy these example bounds into a release entry
without mixed-version qualification. A narrow same-version range is safer than
claiming compatibility that has not been exercised, but cannot by itself support
a rolling upgrade from the previous release.

The release builder embeds the reviewed entry in `release.json`, which is signed
with the rest of the manifest. Older signed releases without this field remain
usable by the existing manual installers and for rollback. The planned managed
upgrade path treats their peer compatibility as unknown until a separately
qualified legacy entry exists.
