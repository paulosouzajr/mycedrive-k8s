#!/bin/bash
# Basic overlayFS layering management

ROOT_LAYER=$2
ROOT_DIR=$3
UP_LAYERS=$(ls /data | grep u)
TAR_LAYERS=$(ls /transfer | grep u)
NEW_LAYER=$(($ROOT_LAYER+1))

cp_upperLayers (){
	#Creates a tar copy of every layer inside overlayFS
	#the copy is saved in the transfer folder to be available to others running containers
	for dir in $UP_LAYERS
	do
		if [[ " ${TAR_LAYERS[*]} " != *" $dir "* ]]; then
			tar -cvf "/transfer/${dir}.tar" --absolute-names "/data/${dir}"
			echo "$dir"
	    fi
	done
	echo "New Layers are ready for transfer"
}

tar_main_flow() {
	tar -cvf "/transfer/u1.tar" --absolute-names ${ROOT_DIR}
}

create_newLayer () {
	#Suspend running processes that might open files
	#for pid in $(ps aux | grep rabbitmq | awk '{print $2}'); do kill -STOP $pid; done

	if [[ $1 -gt 0 ]]; then
		mkdir -p /data/u${NEW_LAYER} /data/w${NEW_LAYER} /data/o${NEW_LAYER}
		mount -t overlay overlay -o lowerdir=/data/o${ROOT_LAYER},upperdir=/data/u${NEW_LAYER},workdir=/data/w${NEW_LAYER} /data/o${NEW_LAYER}

		umount -l ${ROOT_DIR}
		mount --bind /data/o${NEW_LAYER} ${ROOT_DIR}
		cp_upperLayers
	else
		tar_main_flow
	fi

	#Resume running processes
	#for pid in $(ps aux | grep rabbitmq | awk '{print $2}'); do kill -CONT $pid; done
	echo "New layer $NEW_LAYER running"
}

untar_layers (){
	#Select all files inside the transfer folder and untar it to the lower layers of overlayFS
	for tar_file in $TAR_LAYERS
	do
		tar -C "/migration/" -xvf "/transfer/${tar_file}"
	done

	echo "All files have been decompressed"
}


start_overlay (){
	untar_layers

	mkdir -p /data/u1 /data/w1 /data/o1

	LOW_LAYERS=$(ls -d /migration/* | tr "\n." ':')

	mount -t overlay overlay -o lowerdir=${LOW_LAYERS::-1},upperdir=/data/u1,workdir=/data/w1 /data/o1
	mount --bind ${ROOT_DIR} /data/o1
	echo "Overlay successfully initiated"
}


end_overlay (){
	cp_upperLayers
	for process in $(grep 'overlay' /proc/mounts | awk '{print$2}' | sort -r); do echo $process; umount -l $process; done;
}

"$@"
