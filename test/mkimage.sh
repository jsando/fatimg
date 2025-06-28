#!/bin/sh
set -e
set -x

INSTALLDIR="$1"

if [ -z "$INSTALLDIR" ]; then
  INSTALLDIR="$PWD"
fi

mkdir -p $INSTALLDIR/dist
cat <<"EOF" | docker run -i --rm -v $INSTALLDIR/dist:/data alpine:3.20
set -e
set -x

apk --update add dosfstools mtools parted

fatPartitionMegabytes=2048
fatPartitionBytes="$(( fatPartitionMegabytes * 1024 * 1024 ))"
partitionTableBytes="$(( 1024 * 1024 ))" # 1 MiB
dataPartitionExtra="$(( 15 * 1024 * 1024 ))" # 15 MiB

cd "/data"

# sanity check there's something to package
[ -d disk ]

echo 'Creating fat32 partition ...'
truncate -s "$fatPartitionBytes" part.img
mkfs.vfat -v -b 6 -F 32 -r 512 -R 32 part.img
mcopy -i part.img -s -n -o disk/* ::/
dosfsck -n part.img

echo 'Creating disk image ...'
truncate -s "$partitionTableBytes" disk.img
cat part.img >> disk.img
truncate -s "$(( partitionTableBytes + fatPartitionBytes + dataPartitionExtra ))" disk.img

echo 'Creating partition table ...'
parted --script --align=none disk.img -- \
	mklabel msdos \
	mkpart primary fat32 "${partitionTableBytes}B" "$(( partitionTableBytes + fatPartitionBytes ))B" \
	print

EOF
