#!/bin/sh

if [ ! -e "/dev/mmcblk0" ]; then
	echo "################ emmc driver load fail ###################"
	exit 1
fi

if [ ! -e "/dev/mmcblk0p1" ]; then
	echo -e "n\np\n1\n\n\nw\n" | fdisk /dev/mmcblk0
	echo -e "y" | mkfs.ext4 /dev/mmcblk0p1
fi

fdisk -l

if [ ! -e "/dev/mmcblk0p1" ]; then
	echo "############## emmc partition error ###################"
	exit 2
fi

updir=/update

mkdir $updir
mount /dev/mmcblk0p1 $updir

if [ -e "/mnt/update/boot.cfg" ]; then
	cp /mnt/update/boot.cfg $updir
	sync
fi

#if [ -e "/mnt/update/vmlinuz" ]; then
#	cp /mnt/update/vmlinuz $updir
#	sync
#fi

#if [ -e "/mnt/update/loongos-qt.cpio.gz" ]; then
#	cp /mnt/update/loongos-qt.cpio.gz $updir
#	sync
#	cd $updir
#	gzip -d loongos-qt.cpio.gz
#	sleep 50
#	cpio -idmv < loongos-qt.cpio
#	sleep 60
#	cd -
#fi

zcat /mnt/update/loongos-qt.cpio.gz| { cd $updir; cpio -idu ; }

if [ -e "/mnt/update/vmlinuz" ]; then
	cp /mnt/update/vmlinuz /update/boot/.
	sync
fi

if [ -e "/mnt/update/ddr_app.sh" ]; then
	cp /mnt/update/ddr_app.sh $updir
	sync
fi

#if [ -e "/mnt/update/daoli_test.sh" ]; then
#        cp /mnt/update/daoli_test.sh $updir
#        sync
#fi

if [ -e "/mnt/update/stressapptest" ]; then
	cp /mnt/update/stressapptest $updir
	sync
fi

echo "########### Update successfully ##################"
#date -s "2024-04-15 15:00:00"
#sleep 1
#hwclo
