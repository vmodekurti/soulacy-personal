#!/usr/bin/env python3
"""Offline check of built documentation links, anchors, and private exclusions."""
from html.parser import HTMLParser
from pathlib import Path
from urllib.parse import unquote, urlsplit
import sys


class Page(HTMLParser):
    def __init__(self, text):
        super().__init__()
        self.ids = set()
        self.links = []
        self.feed(text)

    def handle_starttag(self, tag, attrs):
        attrs = dict(attrs)
        if attrs.get("id"):
            self.ids.add(attrs["id"])
        if tag == "a" and attrs.get("name"):
            self.ids.add(attrs["name"])
        for key in ("href", "src"):
            if attrs.get(key):
                self.links.append(attrs[key])


def check(root):
    root = root.resolve()
    pages = {path: Page(path.read_text(encoding="utf-8")) for path in root.rglob("*.html")}
    errors = set()
    if not pages or root.joinpath("index.html") not in pages:
        return ["Build the site first: make docs-build"], 0
    for path in root.rglob("*"):
        relative = path.relative_to(root)
        if ("validation" in relative.parts or "brand" in relative.parts
                or "_VALIDATION_" in path.name or "PRODUCTION_RELEASE_" in path.name
                or "LIVING_CORE_RELEASE_" in path.name):
            errors.add(f"Private/operator artifact published: {relative}")
    for source, page in pages.items():
        for link in page.links:
            url = urlsplit(link)
            if url.scheme or url.netloc:
                if url.scheme not in ("http", "https") or url.netloc != "docs.soulacy.io":
                    continue
            target_path = unquote(url.path)
            target = ((root / target_path.lstrip("/")) if target_path.startswith("/")
                      else (source.parent / target_path) if target_path else source).resolve()
            if not target.is_relative_to(root):
                errors.add(f"{source.relative_to(root)}: link escapes site: {link}")
                continue
            if target.is_dir():
                target = target / "index.html"
            if not target.is_file():
                errors.add(f"{source.relative_to(root)}: missing target: {link}")
            elif url.fragment and target in pages and unquote(url.fragment) not in pages[target].ids:
                errors.add(f"{source.relative_to(root)}: missing anchor: {link}")
    return sorted(errors), len(pages)


if __name__ == "__main__":
    problems, count = check(Path(sys.argv[1] if len(sys.argv) > 1 else "site"))
    for problem in problems:
        print(problem)
    print(f"Checked {count} HTML pages; {len(problems)} errors.")
    sys.exit(bool(problems))
