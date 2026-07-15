import { TextInput, type TextInputProps } from "@mantine/core";
import { cn } from "../../lib/utils";

type InputProps = TextInputProps & {
  inputClassName?: string;
};

export function Input({ className, classNames, inputClassName, ...props }: InputProps) {
  const mergedClassNames = typeof classNames === "function"
    ? classNames
    : { ...classNames, input: cn("w-full", classNames?.input, inputClassName) };

  return (
    <TextInput
      className={cn("w-full", className)}
      classNames={mergedClassNames}
      {...props}
    />
  );
}
