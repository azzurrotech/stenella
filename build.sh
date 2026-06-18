#!/bin/sh
set -e

ROOT=$(dirname "$0")
cd "$ROOT"

APP_ID="tech.azzurro.stenella"
BINARY="stenella"

echo "=== Stenella Build Script ==="
echo ""

case "${1:-all}" in
linux|Linux|LINUX)
	echo "Building for Linux..."
	go build -o "$BINARY" .
	echo "  -> $BINARY"
	;;

web|Web|WEB)
	echo "Building for Web (WASM)..."
	fyne package -os web --appID "$APP_ID"
	echo "  -> wasm/"
	;;

android|Android|ANDROID)
	echo "Building for Android..."
	if [ -z "$ANDROID_NDK_HOME" ] && [ -z "$ANDROID_HOME" ]; then
		echo "ERROR: Android NDK not found."
		echo "Set ANDROID_HOME or ANDROID_NDK_HOME to the NDK path."
		echo ""
		echo "Quick setup:"
		echo "  1. Install Android Studio or command-line tools"
		echo "  2. Set ANDROID_HOME to your SDK path"
		echo "  3. Install NDK via SDK Manager"
		echo "  4. Run: sdkmanager 'ndk;25.2.9519653'"
		exit 1
	fi
	fyne package -os android --appID "$APP_ID"
	echo "  -> stenella.apk"
	;;

all|"")
	echo "Building for all targets..."
	echo ""
	echo "--- Linux ---"
	go build -o "$BINARY" .
	echo "  -> $BINARY"
	echo ""
	echo "--- Web ---"
	fyne package -os web --appID "$APP_ID"
	echo "  -> wasm/"
	echo ""
	echo "--- Android ---"
	if [ -n "$ANDROID_NDK_HOME" ] || [ -n "$ANDROID_HOME" ]; then
		fyne package -os android --appID "$APP_ID"
		echo "  -> stenella.apk"
	else
		echo "  SKIPPED (Android NDK not configured)"
		echo "  Set ANDROID_NDK_HOME or ANDROID_HOME to enable."
	fi
	;;

*)
	echo "Usage: $0 [linux|web|android|all]"
	echo ""
	echo "Targets:"
	echo "  linux    - Native Linux binary"
	echo "  web      - WebAssembly (wasm/)"
	echo "  android  - Android APK (requires NDK)"
	echo "  all      - Build everything possible"
	exit 1
	;;
esac
