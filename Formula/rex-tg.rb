class RexTg < Formula
  desc "Bridge coding agents in Rex terminal panes to Telegram"
  homepage "https://github.com/arrokh/rex-tg"
  # A new release: push a tag, run `make formula-release TAG=<tag>`, and
  # put its URL, tag, and revision here. Homebrew clones the pinned Git tag.
  url "https://github.com/arrokh/rex-tg.git",
      tag:      "v0.6.18",
      revision: "319291f8cf561b05a9149b56b0b727a6d3a81ec7"
  license "MIT"
  head "https://github.com/arrokh/rex-tg.git", branch: "main"

  depends_on "go" => :build
  depends_on :macos

  def install
    ldflags = "-s -w -X main.version=#{version}"
    system "go", "build", *std_go_args(ldflags:), "./cmd/rex-tg"
  end

  def caveats
    <<~EOS
      Next steps:
        rex-tg login      # store the bot token in the Keychain, print a pairing code
        rex-tg install    # add agent integrations, skills, and the launchd job
        # then send /start <code> to your bot, and run: rex-tg doctor

      If you installed rex-tg before with `make install`, run
        #{opt_bin}/rex-tg install
        ln -sf #{opt_bin}/rex-tg ~/.local/bin/rex-tg
      The link keeps agents that run already working; they read their hooks
      when they started. Then everything uses this one binary.
      After `brew upgrade rex-tg`, the daemon restarts itself within about
      a minute, and the topics stay linked. Run `rex-tg install` again when
      release notes say agent integrations or skills changed.
      `rex-tg doctor` checks them.

      The Mini App (optional, [miniapp] url) opens only inside your tailnet:
        tailscale serve --bg --https=8443 http://127.0.0.1:8770
      Use `tailscale serve`, not Funnel, and keep Tailscale on on your phone.
      Since 0.4, rex-tg refuses Funnel requests. `rex-tg doctor` checks the
      whole path, from the config to a live request.
    EOS
  end

  test do
    assert_match "rex-tg", shell_output("#{bin}/rex-tg version")
  end
end
