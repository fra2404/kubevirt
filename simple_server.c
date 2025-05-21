#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>
#include <arpa/inet.h>
int main(int argc, char *argv[]) {
    int port = 5901;
    if (argc > 1) port = atoi(argv[1]);
       
    int server_fd = socket(AF_INET, SOCK_STREAM, 0);
    struct sockaddr_in address;
    address.sin_family = AF_INET;
    address.sin_addr.s_addr = INADDR_ANY;
    address.sin_port = htons(port);
    
    bind(server_fd, (struct sockaddr *)&address, sizeof(address));
    printf("Listening on port %d\n", port);
    listen(server_fd, 3);
    
    while(1) {
        struct sockaddr_in client;
        socklen_t client_len = sizeof(client);
        int client_fd = accept(server_fd, (struct sockaddr *)&client, &client_len);
        
        char client_ip[INET_ADDRSTRLEN];
        inet_ntop(AF_INET, &client.sin_addr, client_ip, sizeof(client_ip));
        printf("Connection from %s:%d\n", client_ip, ntohs(client.sin_port));
        
        char buffer[1024] = {0};
        read(client_fd, buffer, 1024);
        write(client_fd, "Connected to test server\n", 25);
        close(client_fd);
    }
    
    return 0;
}