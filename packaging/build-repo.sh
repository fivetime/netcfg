#!/usr/bin/env bash
###############################################################################
# build-repo.sh — assemble GPG-signed apt + yum repositories under $SITE_DIR
# from the packages in $INCOMING_DIR, accumulating on top of whatever is already
# published (the caller checks out the existing gh-pages tree into $SITE_DIR).
#
# netcfg ships a CGO_ENABLED=0 static binary, so a single .deb works on all
# Debian/Ubuntu and a single .rpm on all EL — no per-distro ABI split. Hence:
#   apt: one suite "stable"  -> pool/stable/main + dists/stable
#   yum: one tree per arch   -> rpm/<basearch> (no el<N> split)
# Everything is rebuilt from the package pool (no stateful DB), so re-running over
# the accumulated pool always yields a correct, signed repo.
#
# Required env:
#   SITE_DIR      gh-pages working tree (output root, served by GitHub Pages)
#   INCOMING_DIR  has deb/*.deb and rpm/<rpm_arch>/*.rpm
#   GPG_KEY_ID    fingerprint/key id of the imported signing key
#   REPO_URL      public base URL, e.g. https://owner.github.io/repo
# Optional env:
#   GPG_PASSPHRASE, SUITE(=stable), COMPONENT(=main),
#   ARCHES_DEB(="amd64 arm64"), ARCHES_RPM(="x86_64 aarch64"), ORIGIN, LABEL
###############################################################################
set -euo pipefail

SITE="${SITE_DIR:?set SITE_DIR}"
INCOMING="${INCOMING_DIR:?set INCOMING_DIR}"
KEYID="${GPG_KEY_ID:?set GPG_KEY_ID}"
REPO_URL="${REPO_URL:?set REPO_URL, e.g. https://owner.github.io/repo}"
REPO_URL="${REPO_URL%/}"

SUITE="${SUITE:-stable}"
COMPONENT="${COMPONENT:-main}"
ARCHES_DEB="${ARCHES_DEB:-amd64 arm64}"
ARCHES_RPM="${ARCHES_RPM:-x86_64 aarch64}"
ORIGIN="${ORIGIN:-netcfg community packages}"
LABEL="${LABEL:-netcfg}"
GPG_PASSPHRASE="${GPG_PASSPHRASE:-}"

# Non-interactive signing helper (loopback only when a passphrase is provided).
gpg_pass=()
[ -n "$GPG_PASSPHRASE" ] && gpg_pass=(--pinentry-mode loopback --passphrase "$GPG_PASSPHRASE")
gpg_sign() { gpg --batch --yes "${gpg_pass[@]}" -u "$KEYID" "$@"; }

# Copy a package into the hosted pool, skipping anything over GitHub's 100MB
# per-file git limit (a single oversized file fails the whole gh-pages push).
MAX_MB="${MAX_PKG_MB:-99}"
copy_pkg() {  # copy_pkg <dest_dir> <file>
  local dest="$1" f="$2" base sz
  base="$(basename "$f")"
  sz=$(( $(stat -c%s "$f") / 1048576 ))
  if [ "$sz" -gt "$MAX_MB" ]; then
    echo "::warning::skip $base (${sz}MB > ${MAX_MB}MB GitHub per-file limit) — NOT in repo"
    return 0
  fi
  cp -f "$f" "$dest"/
}

###############################################################################
# APT:  <site>/apt/{pool/stable/main, dists/stable/main/binary-<arch>}
###############################################################################
apt_root="$SITE/apt"
pool="$apt_root/pool/$SUITE/$COMPONENT"
mkdir -p "$pool"
if compgen -G "$INCOMING/deb/*.deb" >/dev/null; then
  echo "==> apt[$SUITE]: adding $(ls "$INCOMING/deb"/*.deb | wc -l) incoming .deb"
  for f in "$INCOMING/deb"/*.deb; do copy_pkg "$pool" "$f"; done
fi

if compgen -G "$pool/*.deb" >/dev/null; then
  ( cd "$apt_root"
    for arch in $ARCHES_DEB; do
      bindir="dists/$SUITE/$COMPONENT/binary-$arch"
      mkdir -p "$bindir"
      # -a <arch> keeps this arch + Architecture:all; paths stay relative to apt_root.
      dpkg-scanpackages -a "$arch" "pool/$SUITE/$COMPONENT" > "$bindir/Packages" 2>/dev/null
      gzip -9kf "$bindir/Packages"
      cat > "$bindir/Release" <<EOF
Archive: $SUITE
Suite: $SUITE
Component: $COMPONENT
Origin: $ORIGIN
Label: $LABEL
Architecture: $arch
EOF
    done

    relconf="$(mktemp)"
    cat > "$relconf" <<EOF
APT::FTPArchive::Release::Origin "$ORIGIN";
APT::FTPArchive::Release::Label "$LABEL";
APT::FTPArchive::Release::Suite "$SUITE";
APT::FTPArchive::Release::Codename "$SUITE";
APT::FTPArchive::Release::Architectures "$ARCHES_DEB";
APT::FTPArchive::Release::Components "$COMPONENT";
APT::FTPArchive::Release::Description "$ORIGIN ($SUITE)";
EOF
    apt-ftparchive -c="$relconf" release "dists/$SUITE" > "dists/$SUITE/Release"
    rm -f "$relconf"

    # InRelease (inline sig) + Release.gpg (detached) — apt accepts either.
    rm -f "dists/$SUITE/InRelease" "dists/$SUITE/Release.gpg"
    gpg_sign --clearsign   -o "dists/$SUITE/InRelease"   "dists/$SUITE/Release"
    gpg_sign --detach-sign -a -o "dists/$SUITE/Release.gpg" "dists/$SUITE/Release"
  )
  echo "   apt[$SUITE]: signed"
else
  echo "   (apt[$SUITE]: empty, skipping)"
fi

###############################################################################
# YUM:  <site>/rpm/<basearch>/{*.rpm, repodata/}
###############################################################################
# rpm --addsign drives gpg through these macros; loopback passes the passphrase.
{
  echo "%_signature gpg"
  echo "%_gpg_name $KEYID"
  if [ -n "$GPG_PASSPHRASE" ]; then
    echo "%__gpg_sign_cmd %{__gpg} gpg --batch --no-verbose --no-armor --pinentry-mode loopback --passphrase $GPG_PASSPHRASE --digest-algo sha256 -u \"%{_gpg_name}\" -sbo %{__signature_filename} %{__plaintext_filename}"
  else
    echo "%__gpg_sign_cmd %{__gpg} gpg --batch --no-verbose --no-armor --digest-algo sha256 -u \"%{_gpg_name}\" -sbo %{__signature_filename} %{__plaintext_filename}"
  fi
} > "$HOME/.rpmmacros"

for arch in $ARCHES_RPM; do
  d="$SITE/rpm/$arch"
  mkdir -p "$d"
  if compgen -G "$INCOMING/rpm/$arch/*.rpm" >/dev/null; then
    echo "==> yum[$arch]: adding $(ls "$INCOMING/rpm/$arch"/*.rpm | wc -l) incoming .rpm"
    for f in "$INCOMING/rpm/$arch"/*.rpm; do copy_pkg "$d" "$f"; done
  fi
  if compgen -G "$d/*.rpm" >/dev/null; then
    rpm --addsign "$d"/*.rpm            # re-signing is idempotent
    createrepo_c --update "$d"
    rm -f "$d/repodata/repomd.xml.asc"
    gpg_sign --detach-sign -a "$d/repodata/repomd.xml"   # repo_gpgcheck=1 gate
    echo "   yum[$arch]: signed"
  else
    echo "   (yum[$arch]: empty, skipping)"
  fi
done

###############################################################################
# Public key, landing page, Pages housekeeping
###############################################################################
echo "==> publishing public key + index"
gpg --batch --export --armor "$KEYID" > "$SITE/netcfg-archive-keyring.asc"
cp "$SITE/netcfg-archive-keyring.asc" "$SITE/RPM-GPG-KEY-netcfg"   # conventional rpm name
touch "$SITE/.nojekyll"                                            # serve _/dotted paths verbatim

# Distinct netcfg versions currently in the apt pool (for display only).
versions="$(ls "$pool"/netcfg_*.deb 2>/dev/null | sed -E 's#.*/netcfg_([^_]+)_.*#\1#' | sort -uV || true)"
ver_items=""
for v in $versions; do ver_items="${ver_items}<li><code>${v}</code></li>"; done
[ -z "$ver_items" ] && ver_items="<li>(none yet)</li>"

cat > "$SITE/index.html" <<EOF
<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>netcfg packages (apt / yum)</title>
<style>
 body{font:15px/1.5 system-ui,sans-serif;max-width:860px;margin:2.5rem auto;padding:0 1rem;color:#1a1a1a}
 h1{margin-bottom:.2rem} h2{margin-top:2rem;border-bottom:1px solid #eee;padding-bottom:.3rem}
 code,pre{font-family:ui-monospace,Menlo,Consolas,monospace}
 pre{background:#0d1117;color:#e6edf3;padding:1rem;border-radius:8px;overflow:auto}
 .muted{color:#666}
</style></head><body>
<h1>netcfg packages</h1>
<p class="muted">netplan-compatible Linux network configuration tool —
signed apt &amp; yum repositories. Built for <code>amd64</code> and <code>arm64</code>.
A single static package works across all Debian/Ubuntu (apt) and EL (yum) releases.</p>

<h2>Debian / Ubuntu (apt)</h2>
<pre>curl -fsSL $REPO_URL/netcfg-archive-keyring.asc \\
  | sudo gpg --dearmor -o /usr/share/keyrings/netcfg-archive-keyring.gpg

echo "deb [signed-by=/usr/share/keyrings/netcfg-archive-keyring.gpg] $REPO_URL/apt $SUITE $COMPONENT" \\
  | sudo tee /etc/apt/sources.list.d/netcfg.list

sudo apt-get update
sudo apt-get install netcfg</pre>

<h2>RHEL / Rocky / AlmaLinux / Fedora (yum/dnf)</h2>
<pre>sudo rpm --import $REPO_URL/RPM-GPG-KEY-netcfg

sudo tee /etc/yum.repos.d/netcfg.repo >/dev/null <<REPO
[netcfg]
name=netcfg packages
baseurl=$REPO_URL/rpm/\\\$basearch
enabled=1
gpgcheck=1
repo_gpgcheck=1
gpgkey=$REPO_URL/RPM-GPG-KEY-netcfg
REPO

sudo dnf install netcfg</pre>

<h2>Available versions</h2>
<ul>$ver_items</ul>

<p class="muted">Signing key: <a href="$REPO_URL/netcfg-archive-keyring.asc">netcfg-archive-keyring.asc</a>
&middot; key id <code>$KEYID</code>. Raw package files are also attached to the
<a href="https://github.com/${GITHUB_REPOSITORY:-fivetime/netcfg}/releases">GitHub Releases</a>.</p>
</body></html>
EOF

echo "==> done. Site tree:"
find "$SITE" -maxdepth 3 -type d | sort
