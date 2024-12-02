# soci-wrapper
Build and push a SOCI index in an alternative way.

* You do not need any other dependencies (such as containerd or zlib) installed.
* You can run this binary anywhere such as CodeBuild or Lambda.

This CLI is used in [`deploy-time-build`](https://github.com/tmokmss/deploy-time-build?tab=readme-ov-file#build-soci-index-for-a-container-image), a CDK construct to build and deploy a SOCI index on CDK deployment.

## Usage
Pass 4 arguments to the CLI as below:

```sh
soci-wrapper REPOSITORY_NAME IMAGE_DIGEST AWS_REGION AWS_ACCOUNT
```

Sometimes (depending on AWS credential configuration) you will also have to set `AWS_REGION` environment variable:

```sh
export AWS_REGION=us-west-2 # the region your ECR repository is located at
```

## Build
To build this project, you must install [all the dependencies](https://github.com/awslabs/soci-snapshotter/blob/main/docs/build.md#dependencies) of soci-snapshotter.

```sh
go build

# or by docker
docker run --rm --platform linux/amd64 -v "$PWD":/app -w /app golang:1.22 bash -c \
  "apt-get update && apt-get install -y zlib1g-dev && GOOS=linux GOARCH=amd64 go build -o soci-wrapper"
```

## NOTICE
Most of the code is copied from [cfn-ecr-aws-soci-index-builder](https://github.com/aws-ia/cfn-ecr-aws-soci-index-builder) project.
