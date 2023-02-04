# Use an official Go image as the base image
FROM golang:latest

# Set the working directory to /app
WORKDIR /app

# Copy the main.go file to the container
COPY main.go .

# Build the Go application
RUN go build -o main .

# Run the executable
CMD ["./main"]