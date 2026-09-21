importpath=github.com/daaku/clientproxy
pkgname=$(basename "$importpath")
pkgver=$(git rev-list --count HEAD)
pkgrel=1
pkgdesc='Dials into Caddy to serve origins without inbound connectivity'
arch=('x86_64' 'aarch64')
url="https://$importpath"
license=('MIT')
depends=()
makedepends=('go')

build() {
  cd ..
  export CGO_ENABLED=0
  go build -trimpath -o clientproxy ./cmd/clientproxy
}

package() {
  cd ..
  install -Dm755 clientproxy "$pkgdir/usr/bin/clientproxy"
  install -Dm644 license "$pkgdir/usr/share/licenses/$pkgname/LICENSE"
}
