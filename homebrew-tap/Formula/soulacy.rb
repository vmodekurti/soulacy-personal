# Formula/soulacy.rb
#
# Homebrew tap for Soulacy.
#
# This file lives in a repo named `homebrew-tap` under your GitHub org:
#   https://github.com/soulacy/homebrew-tap
#
# Install with:
#   brew tap soulacy/tap
#   brew install soulacy
#
# Or in one line:
#   brew install soulacy/tap/soulacy
#
# ── Updating this formula after a new release ─────────────────────────────────
# 1. Download the new tarball:
#      curl -Lo /tmp/soulacy.tar.gz https://github.com/soulacy/soulacy/releases/download/vX.Y.Z/soulacy_vX.Y.Z_darwin_arm64.tar.gz
# 2. Get the SHA256:
#      shasum -a 256 /tmp/soulacy.tar.gz
# 3. Update `url`, `sha256`, and `version` below.
# 4. Commit and push — Homebrew picks up the change automatically.
#
# Tip: The GitHub Actions release workflow can auto-update this formula
# via a `brew bump-formula-pr` step or a direct commit to homebrew-tap.

class Soulacy < Formula
  desc "Self-hosted agentic AI framework — privacy-first, runs locally"
  homepage "https://github.com/soulacy/soulacy"
  license "MIT"

  # ── macOS Apple Silicon (primary) ───────────────────────────────────────────
  on_arm do
    url "https://github.com/soulacy/soulacy/releases/download/v0.1.0/soulacy_v0.1.0_darwin_arm64.tar.gz"
    sha256 "REPLACE_WITH_ACTUAL_SHA256_FOR_ARM64"
  end

  # ── macOS Intel ─────────────────────────────────────────────────────────────
  on_intel do
    url "https://github.com/soulacy/soulacy/releases/download/v0.1.0/soulacy_v0.1.0_darwin_amd64.tar.gz"
    sha256 "REPLACE_WITH_ACTUAL_SHA256_FOR_AMD64"
  end

  version "0.1.0"

  # Python 3 is needed to run Python-defined agents and tools.
  # It's pre-installed on macOS and available via Homebrew.
  depends_on "python@3.11" => :recommended

  def install
    bin.install "soulacy"
    bin.install "sy"
  end

  def post_install
    # Create the default data directory if it doesn't exist.
    (var/"soulacy").mkpath
    ohai "Soulacy installed!"
    ohai "Start the gateway:  soulacy serve"
    ohai "Open the GUI:       http://localhost:18789"
    ohai "Config:             #{Dir.home}/.soulacy/config.yaml"
  end

  service do
    # Registers `soulacy serve` as a Homebrew service.
    # Enable with: brew services start soulacy
    run          [opt_bin/"soulacy", "serve"]
    working_dir  var/"soulacy"
    log_path     var/"log/soulacy.log"
    error_log_path var/"log/soulacy-error.log"
    keep_alive   true
    restart_condition :failure
  end

  test do
    # Smoke test: binary runs and prints version.
    assert_match version.to_s, shell_output("#{bin}/soulacy version 2>&1", 0)
    assert_match version.to_s, shell_output("#{bin}/sy version 2>&1", 0)
  end
end
