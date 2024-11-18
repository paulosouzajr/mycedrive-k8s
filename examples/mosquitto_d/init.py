import requests, os, time, json
from os import path

url = 'http://192.168.0.100:5000/register'

data = dict(os.environ)

print(data)

try:
    response = requests.post(url, json=data)

    response.raise_for_status()
except HTTPError as http_err:
    print(f'HTTP error occurred: {http_err}')  # Python 3.6
except Exception as err:
    print(f'Other error occurred: {err}')  # Python 3.6
else:
    print('Success!')


print(response)
sec = 0

while not path.exists('/dmtcp/bin/dmtcp_launch'):
    time.sleep(1)
    sec+=1


print(f'ready: {sec}')
