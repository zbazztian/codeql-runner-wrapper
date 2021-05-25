package main

import (
  "fmt"
  "os/exec"
  "log"
  "os"
  "errors"
  "path/filepath"
  "strings"
  "bufio"
  "io"
  "encoding/json"
  "io/ioutil"
)


const runner_executable = "./codeql-runner-linux"        // path to the codeql-runner-linux executable
const temp_dir = "."                                     // directory in which the runner stores temporary files, such as the analysis database
var tools_dir = resolve_tilde("~/codeql-runner-tools")   // path to the directory where the runner stores the analysis software
const github_url = "https://github.com"                  // the URL to the GitHub server


func resolve_tilde(path string) string {
  user_home, err := os.UserHomeDir(); if err != nil {
    log.Fatal(err)
  }
  if path == "~" {
    return user_home
  } else if strings.HasPrefix(path, "~/") {
    return filepath.Join(user_home, path[2:])
  }
  return path
}

func subprocess(input string, working_dir string, args ...string) {
  log.Print(strings.Join(args, " "))
  cmd := exec.Command(args[0], args[1:]...)
  cmd.Dir = working_dir
  stdin, err := cmd.StdinPipe(); if nil != err {
    log.Fatalf("Error obtaining stdin: %s", err.Error())
  }
  stdout, err := cmd.StdoutPipe(); if nil != err {
    log.Fatalf("Error obtaining stdout: %s", err.Error())
  }
  stderr, err := cmd.StderrPipe(); if nil != err {
    log.Fatalf("Error obtaining stderr: %s", err.Error())
  }
  go func(reader io.Reader) {
    scanner := bufio.NewScanner(reader)
    for scanner.Scan() {
      log.Print(scanner.Text())
    }
  }(bufio.NewReader(stdout))
  go func(reader io.Reader) {
    scanner := bufio.NewScanner(reader)
    for scanner.Scan() {
      log.Print(scanner.Text())
    }
  }(bufio.NewReader(stderr))
  if err := cmd.Start(); nil != err {
    log.Fatalf("Error starting program: %s, %s", args[0], err.Error())
  }
  stdin.Write([]byte(input))
  stdin.Close()
  result := cmd.Wait(); if result != nil {
    log.Fatal("Subprocess failed!")
  }
}

func read_languages(json_path string) []string {
  json_file, err := os.Open(json_path); if err != nil {
    log.Fatal("Unable to open config file!")
  }
  defer json_file.Close()

  json_data, err := ioutil.ReadAll(json_file); if err != nil {
    log.Fatal("Unable to read config file!")
  }

  data := make(map[string]interface{})

  if err := json.Unmarshal(json_data, &data); err != nil {
    log.Fatal("Unable to parse config file!")
  }

  var result []string
  for _, lang := range data["languages"].([]interface{}) {
    result = append(result, lang.(string))
  }

  return result
}

func is_compiled(lang string) bool {
  if lang == "java" || lang == "cpp" || lang == "csharp" {
    return true
  } else {
    return false
  }
}

func analyze(
  checkout_path string,        // the root directory of the repository to analyze
  repository_id string,        // the repository to analyze in the form  "orgname/reponame"
  commit_id string,            // the commit sha of the revision to analyze
  git_ref string,              // the git ref, e.g. "refs/heads/master"
  languages []string,          // an array of languages to analyze, can be empty, in which case the languages are guessed
  build_commands string,       // the build command for the compiled languages. Will be executed in a unix shell. Can be multiple lines long. Can be an empty string, in which case the build commands will be guessed.
  github_token string,         // a GitHub authentication token
) {
  _, err := os.Stat(runner_executable); if errors.Is(err, os.ErrNotExist) {
    log.Fatal(err)
  }

  // initialization
  args := []string {
    runner_executable,
    "init",
    "--temp-dir", temp_dir,
    "--tools-dir", tools_dir,
    "--github-auth-stdin",
    "--github-url", github_url,
    "--repository", repository_id,
    "--checkout-path", checkout_path,
    "--debug",
  }
  if len(languages) > 0 {
    args = append(args, "--languages")
    args = append(args, strings.Join(languages, ","))
  }
  subprocess(
    github_token,
    checkout_path,
    args...
  )


  config_file := filepath.Join(temp_dir, "codeql-runner", "config")
  _, err = os.Stat(config_file); if errors.Is(err, os.ErrNotExist) {
    log.Fatal(err)
  }
  languages = read_languages(config_file)

  if build_commands == "" {
    for _, lang := range languages {
      if is_compiled(lang) {
        subprocess(
          "",
          checkout_path,
          runner_executable,
          "autobuild",
          "--temp-dir", temp_dir,
          "--debug",
          "--language", lang,
        )
      }
    }
  } else {
    env_file := filepath.Join(temp_dir, "codeql-runner", "codeql-env.sh")
    build_script := fmt.Sprintf(`
echo sourcing environment variables...
if [ -f "%s" ]; then
  . "%s"
fi
echo executing custom build commands...
%s
exit
  `, env_file, env_file, build_commands)
    log.Print(build_script)

    subprocess(
      build_script,
      checkout_path,
      "/bin/sh",
      "-s",
    )
  }

  // analysis
  subprocess(
    github_token,
    checkout_path,
    runner_executable,
    "analyze",
    "--github-auth-stdin",
    "--github-url", github_url,
    "--repository", repository_id,
    "--ref", git_ref,
    "--commit", commit_id,
    "category", strings.Join(languages, ","),
  )
}

func main() {

  // Example 1 (pass languages and build commands)
  /*
  analyze(
    resolve_tilde("~/path/to/checkout/dir"),
    "orgname/reponame",
    "c1bbed54ce666845939bba64a622d06ff68f3647",
    "refs/heads/master",
    [] string { "java", "javascript" },
    "cd java\nmvn clean install -DskipTests",
    "1234567890123456789012345678901234567890",
  )
  */

  // Example 2 (only pass languages and rely on autobuild)
  /*
  analyze(
    resolve_tilde("~/path/to/checkout/dir"),
    "orgname/reponame",
    "c1bbed54ce666845939bba64a622d06ff68f3647",
    "refs/heads/master",
    [] string { "java", "javascript" },
    "",                // no build commands given
    "1234567890123456789012345678901234567890",
  )
  */

  // Example 3 (autodetect languages and rely on autobuild detection)
  /*
  analyze(
    resolve_tilde("~/path/to/checkout/dir"),
    "orgname/reponame",
    "c1bbed54ce666845939bba64a622d06ff68f3647",
    "refs/heads/master",
    [] string {},                // no languages given
    "",                          // no build commands given
    "1234567890123456789012345678901234567890",
  )
  */
}
