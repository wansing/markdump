# markdump

markdump serves a dump of markdown files via web.

## Features

* **No additional markup**: Just dump your markdown files and folders. No YAML header or whatever.
* **Read-only**: You can use a git frontend for editing content, e. g. [Gitea](https://github.com/go-gitea/gitea).
* **Search**: Very basic live search function.
* **Token Authentication**: Configure one or multiple access tokens, including `public`, and create shareable links for any page with one click.

## Configuration via Environment Variables

* `AUTH`: list of authentication tokens, separated by whitespaces
* `LISTEN`: HTTP listen address, default: `127.0.0.1:8134`
* `RELOAD_SECRET`: secret for git reload handler, default: randomly generated and printed to stderr
* `REPO`: path to content folder, default: `.`
* `TITLE`: title for root content folder, default: `Home`

## Try it

```
git clone https://github.com/wansing/markdump.git
cd markdump
go build ./cmd/markdump
AUTH=public RELOAD_SECRET=change-me REPO=content ./markdump
```

Then call the reload URL `http://127.0.0.1:8134/reload?secret=change-me`. It will output `git reload failed: git reload has no effect when running in a terminal` because we don't want to mess with git repositories in interactive scenarios.
