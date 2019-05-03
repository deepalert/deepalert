#!/usr/bin/env python

import os
import argparse
import json

required_parameters = [
    'StackName',
    'Region',
    'CodeS3Bucket',
    'CodeS3Prefix',
]
optional_parameters = [
    'LambdaRoleArn',
    'StepFunctionRoleArn',
    'ReviewerLambdaArn',
    'InspectionDelay',
    'ReviewDelay',
]


def args2config(args):
    params = required_parameters + optional_parameters

    if args.config:
        config = dict([(k, v) for k, v in json.load(args.config).items()])
    else:
        config = {}

    for k in params:
        if hasattr(args, k) and getattr(args, k) is not None:
            config[k] = getattr(args, k)

    return config


def get_functions():
    basedir = './functions'
    return filter(lambda x: os.path.isdir(os.path.join(basedir, x)),
                  os.listdir(basedir))


def gen_functions_section():
    template = '''build/{0}: ./functions/{0}/*.go $(COMMON)
	env GOARCH=amd64 GOOS=linux go build -o build/{0} ./functions/{0}/'''

    lines = map(lambda x: template.format(x), get_functions())
    return '\n'.join(lines) + '\n'


def get_test_functions():
    basedir = './test'
    return filter(lambda x: os.path.isdir(os.path.join(basedir, x)),
                  os.listdir(basedir))


def gen_test_functions_section():
    template = '''build/{0}: ./test/{0}/*.go
	env GOARCH=amd64 GOOS=linux go build -o build/{0} ./test/{0}/'''

    test_function_builds = [template.format(x) for x in get_test_functions()]

    return '\n'.join(test_function_builds + [''])


def gen_header(args):
    template = '''
TEMPLATE_FILE=template.yml
TEST_TEMPLATE_FILE=test.yml
SAM_FILE={2}
OUTPUT_FILE={3}
TEST_SAM_FILE={4}
TEST_OUTPUT_FILE={5}

COMMON=functions/*.go *.go
FUNCTIONS={0}
TEST_FUNCTIONS={1}
'''
    sam_file = os.path.join(args.workdir, 'sam.yml')
    output_file = os.path.join(args.workdir, 'output.json')
    test_sam_file = os.path.join(args.workdir, 'test_sam.yml')
    test_output_file = os.path.join(args.workdir, 'test_output.json')

    functions = map(lambda f: os.path.join('build', f), get_functions())
    test_functions = map(lambda f: os.path.join(
        'build', f), get_test_functions())

    return template.format(' '.join(functions), ' '.join(test_functions),
                           sam_file, output_file,
                           test_sam_file, test_output_file)


def gen_parameters(config):
    lines = []

    # Check required parameters
    for k in required_parameters:
        if k not in config.keys():
            raise Exception(
                'Parameter "{0}" is required, but not found'.format(k))

        lines.append('{0}="{1}"'.format(k, config[k]))

    # Build override parameters
    options = []
    for k in optional_parameters:
        if k in config.keys():
            options.append('{0}="{1}"'.format(k, config[k]))

    opt = ' '.join(["--parameter-overrides"] + options) if options else ''

    lines.append('PARAMETERS={0}'.format(opt))

    return '\n'.join(lines) + '\n'


def gen_task_section(config):
    return '''
functions: $(FUNCTIONS)

clean:
	rm $(FUNCTIONS)

$(SAM_FILE): $(TEMPLATE_FILE) $(FUNCTIONS)
	mkdir -p `dirname $(SAM_FILE)`
	aws cloudformation package \\
		--template-file $(TEMPLATE_FILE) \\
		--s3-bucket $(CodeS3Bucket) \\
		--s3-prefix $(CodeS3Prefix) \\
		--output-template-file $(SAM_FILE)

$(OUTPUT_FILE): $(SAM_FILE)
	aws cloudformation deploy \\
		--region $(Region) \\
		--template-file $(SAM_FILE) \\
		--stack-name $(StackName) \\
		--capabilities CAPABILITY_IAM $(PARAMETERS)
	aws cloudformation describe-stack-resources --stack-name $(StackName) > $(OUTPUT_FILE)

deploy: $(OUTPUT_FILE)

test: $(OUTPUT_FILE)
	env DEEPALERT_TEST_CONFIG=$(OUTPUT_FILE) go test -v ./...


$(TEST_SAM_FILE): $(TEST_TEMPLATE_FILE) $(TEST_FUNCTIONS)
	mkdir -p `dirname $(TEST_SAM_FILE)`
	aws cloudformation package \\
		--template-file $(TEST_TEMPLATE_FILE) \\
		--s3-bucket $(CodeS3Bucket) \\
		--s3-prefix $(CodeS3Prefix) \\
		--output-template-file $(TEST_SAM_FILE)

$(TEST_OUTPUT_FILE): $(TEST_SAM_FILE)
	aws cloudformation deploy \\
		--region $(Region) \\
		--template-file $(TEST_SAM_FILE) \\
		--stack-name $(StackName)-test \\
		--capabilities CAPABILITY_IAM \\
        --parameter-overrides ParentStackName=$(StackName)
	aws cloudformation describe-stack-resources --stack-name $(StackName)-test > $(TEST_OUTPUT_FILE)

setuptest: $(TEST_OUTPUT_FILE)
'''


def main():
    psr = argparse.ArgumentParser()
    psr.add_argument('-o', '--output', default="Makefile")
    psr.add_argument('-c', '--config', type=argparse.FileType('rt'))
    psr.add_argument('-w', '--workdir', default=".")

    psr.add_argument('--StackName')
    psr.add_argument('--Region')
    psr.add_argument('--CodeS3Bucket')
    psr.add_argument('--CodeS3Prefix')
    psr.add_argument('--LambdaRoleArn')
    psr.add_argument('--StepFunctionRoleArn')
    psr.add_argument('--ReviewerLambdaArn')
    psr.add_argument('--InspectionDelay', type=int)
    psr.add_argument('--ReviewDelay', type=int)

    args = psr.parse_args()
    config = args2config(args)

    body = [
        gen_header(args),
        gen_parameters(config),
        gen_task_section(config),
        gen_functions_section(),
        gen_test_functions_section(),
    ]

    makefile = '\n'.join(body)
    if args.output != "-":
        open(args.output, 'wt').write(makefile)
    else:
        print(makefile)


if __name__ == '__main__':
    main()
