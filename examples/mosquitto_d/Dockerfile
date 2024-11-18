FROM ubuntu:18.04

RUN apt-get update 
RUN apt-get install -y python3 python3-pip mosquitto mosquitto-clients && python3 -m pip install requests

VOLUME ["/mosquitto/data", "/mosquitto/log", "/dmtcp"]

# Set up the entry point script and default command
COPY docker-entrypoint.sh /
COPY init.py /

RUN chmod 777 /docker-entrypoint.sh
ENTRYPOINT ["/docker-entrypoint.sh"]

EXPOSE 1883 9001

#CMD ["/dmtcp/bin/dmtcp_launch", "-j", "/usr/sbin/mosquitto"]
#CMD ["sleep", "100000"]


