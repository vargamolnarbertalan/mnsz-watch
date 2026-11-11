# mnsz-watch

Watches program pages on [mnsz.jegy.hu](https://mnsz.jegy.hu) (Miskolci Nemzeti Színház) and sends an urgent
[ntfy](https://ntfy.sh) push the moment tickets for a chosen performance date go on sale.

The image is a static Go binary on `scratch` (about 9 MB). Every push to `main` runs the tests and publishes
`ghcr.io/vargamolnarbertalan/mnsz-watch:latest` (plus a `:<commit sha>` tag).

## Run

A target is `<program-slug>@<YYYY-MM-DD>`, the slug being the last part of the program URL
(`https://mnsz.jegy.hu/program/banyavirag-176949` → `banyavirag-176949`). The ntfy topic is the only secret: it
is read from the shell without echo, so it lands in neither the shell history nor any file.

```sh
read -rsp 'ntfy topic: ' NTFY_TOPIC && export NTFY_TOPIC

docker run -d --name mnsz-watch --restart unless-stopped \
  -e NTFY_TOPIC \
  --read-only --cap-drop ALL --security-opt no-new-privileges \
  --memory 32m --cpus 0.1 --log-opt max-size=1m --log-opt max-file=3 \
  ghcr.io/vargamolnarbertalan/mnsz-watch:latest \
  -poll=60s banyavirag-176949@2026-12-15 sararany-192632@2027-01-19
```

## Change targets or update the image

Take the topic from the running container, then replace it:

```sh
export NTFY_TOPIC="$(docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' mnsz-watch | sed -n 's/^NTFY_TOPIC=//p')"
docker pull ghcr.io/vargamolnarbertalan/mnsz-watch:latest
docker rm -f mnsz-watch
# then the docker run command above, with the new targets
```

## Check and debug

```sh
docker logs --tail 50 mnsz-watch                       # one line per check
docker run --rm ghcr.io/vargamolnarbertalan/mnsz-watch:latest -once sararany-192632@2027-01-19   # no push
```

## Behaviour

- On start it sends a low-priority push listing the watched dates, which proves the topic works.
- When a date goes on sale: one urgent push with a link to the purchase page, then that date is done. A sold-out
  date ("Elfogyott") counts as not on sale, so returned tickets still trigger the push.
- After 30 failed checks in a row for a date (site down, or the date missing from the page, e.g. after a
  redesign or a cancelled show), one warning push, so the watcher cannot go blind silently.
- After the performance date has passed it stops checking that date.
- When every date is done, the container stays up idle: exiting would make `--restart unless-stopped` start it
  again and repeat the pushes. Remove it with `docker rm -f mnsz-watch`.
- A container restart (e.g. a host reboot) starts from scratch: a new startup push, and a repeat of the urgent
  push for a date that is still on sale.
