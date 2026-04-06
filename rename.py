import os
import sys

def replace_in_file(filepath):
    try:
        with open(filepath, 'r') as f:
            content = f.read()
    except Exception as e:
        return

    # Replacements
    new_content = content.replace("mini-docker", "minibox")
    new_content = new_content.replace("MINI_DOCKER", "MINIBOX")

    if new_content != content:
        with open(filepath, 'w') as f:
            f.write(new_content)
        print(f"Updated {filepath}")

def main():
    root_dir = "/home/chaitanya/projects/Documents/mini-docker"
    
    for dirpath, dirnames, filenames in os.walk(root_dir):
        # don't traverse into excluded dirs
        dirnames[:] = [d for d in dirnames if d not in {'.git', 'bin', 'data', '.gemini'}]

        for filename in filenames:
            if filename.endswith(('.so', '.gz', '.tar', '.o')) or filename in {'daemon.log', 'minibox-cli', 'minibox-daemon'}:
                continue
            
            filepath = os.path.join(dirpath, filename)
            replace_in_file(filepath)

if __name__ == "__main__":
    main()
