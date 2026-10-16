#!/bin/sh
# Builds LANdrop.apk with plain SDK tools (no Gradle). Needs build-tools;35.0.0 and platforms;android-35.
set -e
cd "$(dirname "$0")"
SDK=${ANDROID_HOME:-$HOME/Library/Android/sdk}
BT=$SDK/build-tools/35.0.0
JAR=$SDK/platforms/android-35/android.jar
rm -rf build && mkdir -p build/classes
$BT/aapt2 compile --dir res -o build/res.zip
$BT/aapt2 link -I $JAR --manifest AndroidManifest.xml -o build/unsigned.apk build/res.zip
javac --release 11 -cp $JAR -d build/classes $(find src -name '*.java')
$BT/d8 --min-api 29 --lib $JAR --output build $(find build/classes -name '*.class')
(cd build && zip -q unsigned.apk classes.dex)
$BT/zipalign -f 4 build/unsigned.apk build/aligned.apk
[ -f debug.keystore ] || keytool -genkeypair -keystore debug.keystore -storepass android -keypass android -alias landrop -keyalg RSA -validity 10000 -dname CN=LANdrop
$BT/apksigner sign --ks debug.keystore --ks-pass pass:android --out LANdrop.apk build/aligned.apk
echo built $(pwd)/LANdrop.apk
