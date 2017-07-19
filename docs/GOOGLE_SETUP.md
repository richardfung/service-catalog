# Prerequisites
## Build the service catalog
You will need to make the repo with the changes and then change the helm charts
to use your images.

From the root of this repo, to build and push the images, run

```
export REGISTRY=<GCR_URL>

make build
make images push
```

Now, modify the helm chart so that it uses your image. You will need to modify
charts/catalog/values.yaml so that all the images point to your images instead
of the things in quay.io.

For example, for a project `test-project`, you would change

```
  image: quay.io/kubernetes-service-catalog/apiserver:v0.0.11
```

to be

```
  image: us.gcr.io/test-project/apiserver
```

and do the same for the controller manager image as well which as this writing is

```
  image: quay.io/kubernetes-service-catalog/controller-manager:v0.0.11
```

Start a Kubernetes cluster and install the service catalog (follow walkthrough.md)

# Register the broker
## Generate a Service Account Key
In Pantheon, go to API Manager/Credentials and click create credentials and
Service account key. Create a Compute Engine default service account with the
JSON key type.

Then, you will need to do a base64 encode of that file and replace the jwt
section in the demo/secrets.yaml file. You can do so with

```
base64 <path to service account key file>
```

You can also change the scopes if you need to. As of this writing the scope is
set to `["https://www.googleapis.com/auth/cloud-platform"]`

Make sure to delete the random newlines that are created when base64 encoding
as I think it interferes with the yaml parsing.  Now create the secret

```
kubectl create -f demo/secret.yaml
```

Now we need to add the broker.

Modify the demo/brokers.yaml file so that the url matches your desired broker.
Then, create the broker via

```
kubectl --context=service-catalog create -f demo/brokers.yaml
```

You can see the outcome with

```
kubectl --context=service-catalog get brokers -oyaml
```

If it worked, you should be able to see the service classes with

```
kubectl --context=service-catalog get serviceclasses
```
