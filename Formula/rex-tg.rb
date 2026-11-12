class RexTg < Formula
  desc "Bridge coding agents in Rex terminal panes to Telegram"
  homepage "https://github.com/arrokh/rex-tg"
  head "https://github.com/arrokh/rex-tg.git", branch: "main"

  # Stable release: after pushing a tag, run `make formula-release TAG=v0.1.0`
  # and put its url lines here, above `head`. The repo is private, so the
  # formula clones the tag with git (the user's own GitHub login) instead of
  # downloading an archive.

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
        rex-tg install    # add the hooks and the launchd job
        # then send /start <code> to your bot, and run: rex-tg doctor

      If you installed rex-tg before with `make install`, remove
      ~/.local/bin/rex-tg, or run `rex-tg install` again from this binary.
      Then the hooks and launchd point at one binary.
      After `brew upgrade rex-tg`, the daemon restarts itself within about
      a minute, and the topics stay linked. Run `rex-tg install` again only
      when the release notes say the hooks changed. `rex-tg doctor` checks it.
    EOS
  end

  test do
    assert_match "rex-tg", shell_output("#{bin}/rex-tg version")
  end
end
